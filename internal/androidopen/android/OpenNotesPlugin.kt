package dev.mygo

import android.app.Activity
import android.content.ActivityNotFoundException
import android.content.ContentResolver
import android.content.Context
import android.content.Intent
import android.graphics.Bitmap
import android.graphics.Color
import android.graphics.Matrix
import android.graphics.pdf.PdfRenderer
import android.net.ConnectivityManager
import android.net.Uri
import android.os.Build
import android.os.ParcelFileDescriptor
import android.provider.OpenableColumns
import android.provider.Settings
import androidx.core.content.FileProvider
import java.io.File
import java.io.IOException
import java.nio.file.Files
import java.util.Locale
import java.util.concurrent.ConcurrentLinkedQueue
import java.util.concurrent.ExecutorService
import java.util.concurrent.Executors
import java.util.concurrent.ScheduledExecutorService
import java.util.concurrent.ScheduledFuture
import java.util.concurrent.TimeUnit
import kotlin.math.roundToInt
import org.json.JSONObject

class OpenNotesPlugin : MyGoPlugin {
    private val stylus = StylusBridge()
    private val pending = ConcurrentLinkedQueue<JSONObject>()
    private val worker = Executors.newSingleThreadExecutor()
    private val pdfForeground = Executors.newSingleThreadExecutor()
    private val pdfBackground = Executors.newSingleThreadExecutor()
    private val pdfIdleScheduler: ScheduledExecutorService = Executors.newSingleThreadScheduledExecutor()
    private val fgPdfSlot = PdfRendererSlot(pdfForeground)
    private val bgPdfSlot = PdfRendererSlot(pdfBackground)
    @Volatile
    private var deferredInstallPath: String? = null

    init {
        PluginHost.onIntent = ::open
        LaunchCover.install(PluginHost.context, PluginHost.activity)
        LaunchCover.setOnActivityResumedListener(::resumeDeferredInstall)
        worker.execute { cleanupStaleTempDirs() }
    }

    override fun call(method: String, args: JSONObject, reply: PluginReply) {
        when (method) {
            "take" -> reply.success(pending.poll())
            "setAppearance" -> {
                LaunchCover.setAppearance(args.optString("appearance", ""))
                reply.success(null)
            }
            "frameReady" -> {
                LaunchCover.onFrameReady()
                reply.success(null)
            }
            "penZones" -> {
                stylus.update(args.getJSONArray("zones"))
                reply.success(null)
            }
            "renderPDF" -> {
                val background = args.optBoolean("background")
                val renderer = if (background) pdfBackground else pdfForeground
                val slot = if (background) bgPdfSlot else fgPdfSlot
                renderer.execute {
                    try {
                        reply.success(renderPDF(args, slot))
                    } catch (err: Exception) {
                        reply.success(JSONObject().put("error", err.localizedMessage ?: "无法渲染 PDF"))
                    }
                }
            }
            "closePDFCache" -> {
                fgPdfSlot.closeAsync()
                bgPdfSlot.closeAsync()
                reply.success(null)
            }
            "updateCacheDir" -> {
                try {
                    reply.success(JSONObject().put("dir", ensureUpdateCacheDir().absolutePath))
                } catch (err: PluginFailure) {
                    reply.failure(err)
                } catch (err: Exception) {
                    reply.failure(PluginFailure("UpdateCacheDirFailed", err.localizedMessage ?: "无法准备更新缓存目录"))
                }
            }
            "canInstallAPK" -> {
                try {
                    reply.success(JSONObject().put("allowed", canInstallAPK(PluginHost.context)))
                } catch (err: PluginFailure) {
                    reply.failure(err)
                } catch (err: Exception) {
                    reply.failure(PluginFailure("CanInstallAPKFailed", err.localizedMessage ?: "无法检查安装权限"))
                }
            }
            "systemProxy" -> {
                reply.success(JSONObject().put("url", systemProxyUrl(PluginHost.context)))
            }
            "installAPK" -> {
                try {
                    reply.success(installAPK(args))
                } catch (err: PluginFailure) {
                    reply.failure(err)
                } catch (err: Exception) {
                    reply.failure(PluginFailure("InstallFailed", err.localizedMessage ?: "无法启动系统安装程序"))
                }
            }
            else -> reply.failure(PluginFailure("UnknownMethod"))
        }
    }

    private fun ensureUpdateCacheDir(): File {
        val context = PluginHost.context
        val dir = File(context.cacheDir, "updates")
        if (!dir.isDirectory && !dir.mkdirs() && !dir.isDirectory) {
            throw IOException("无法创建更新缓存目录")
        }
        val canonicalCache = context.cacheDir.canonicalFile
        val canonicalDir = dir.canonicalFile
        if (!canonicalDir.isDirectory || canonicalDir.parentFile != canonicalCache || canonicalDir.name != "updates") {
            throw IOException("更新缓存目录无效")
        }
        return canonicalDir
    }

    private fun canInstallAPK(context: Context): Boolean =
        Build.VERSION.SDK_INT < Build.VERSION_CODES.O ||
            context.packageManager.canRequestPackageInstalls()

    private fun systemProxyUrl(context: Context): String {
        val defaultProxy = try {
            val cm = context.getSystemService(Context.CONNECTIVITY_SERVICE) as? ConnectivityManager
            cm?.defaultProxy
        } catch (_: SecurityException) {
            null
        } catch (_: Exception) {
            null
        }
        if (defaultProxy != null) {
            formatProxyUrl(defaultProxy.host, defaultProxy.port)?.let { return it }
        }
        for ((hostKey, portKey) in arrayOf(
            "http.proxyHost" to "http.proxyPort",
            "https.proxyHost" to "https.proxyPort",
        )) {
            val host = runCatching { System.getProperty(hostKey) }.getOrNull()
            val port = runCatching { System.getProperty(portKey) }.getOrNull()?.trim()?.toIntOrNull()
            if (port != null) {
                formatProxyUrl(host, port)?.let { return it }
            }
        }
        return ""
    }

    private fun formatProxyUrl(rawHost: String?, port: Int): String? {
        val host = rawHost?.trim().orEmpty()
        if (host.isEmpty() || port !in 1..65535) return null
        for (ch in host) {
            if (ch <= ' ' || ch.code == 0x7F || ch == '/' || ch == '\\' || ch == '?' || ch == '#' || ch == '@') {
                return null
            }
        }
        val firstColon = host.indexOf(':')
        val formattedHost = when {
            firstColon < 0 -> {
                if (host.indexOf('[') >= 0 || host.indexOf(']') >= 0) return null
                host
            }
            host.startsWith('[') && host.endsWith(']') -> {
                val inner = host.substring(1, host.length - 1)
                if (inner.isEmpty() || inner.indexOf('[') >= 0 || inner.indexOf(']') >= 0) return null
                host
            }
            firstColon != host.lastIndexOf(':') -> {
                if (host.indexOf('[') >= 0 || host.indexOf(']') >= 0) return null
                "[$host]"
            }
            else -> return null
        }
        return "http://$formattedHost:$port"
    }

    private fun validateAPKFile(rawPathInput: String): File {
        val rawPath = rawPathInput.trim()
        if (rawPath.isEmpty()) {
            throw PluginFailure("InvalidPath", "更新安装包路径为空")
        }
        val updatesDir = ensureUpdateCacheDir()
        val updatesPath = updatesDir.toPath()
        val requestedFile = File(rawPath)
        if (!requestedFile.exists()) {
            throw PluginFailure("NotFound", "更新安装包不存在")
        }
        val apkFile = requestedFile.canonicalFile
        val apkPath = apkFile.toPath()
        if (apkPath == updatesPath || !apkPath.startsWith(updatesPath)) {
            throw PluginFailure("InvalidPath", "更新安装包路径不在允许的更新目录内")
        }
        if (!Files.isRegularFile(apkPath) || apkFile.length() <= 0L ||
            !apkFile.name.lowercase(Locale.ROOT).endsWith(".apk")
        ) {
            throw PluginFailure("InvalidAPK", "更新文件不是有效的 APK 安装包")
        }
        return apkFile
    }

    private fun installAPK(args: JSONObject): JSONObject {
        val apkFile = validateAPKFile(args.optString("path", ""))
        val context = PluginHost.context
        val activity = LaunchCover.currentResumedActivity()
        if (activity == null) {
            deferredInstallPath = apkFile.absolutePath
            return JSONObject().put("status", "deferred_until_foreground")
        }
        deferredInstallPath = null
        if (!canInstallAPK(context)) {
            launchUnknownSourcesSettings(activity, context)
            return JSONObject().put("status", "permission_required")
        }
        launchAPKInstaller(activity, context, apkFile)
        return JSONObject().put("status", "launched")
    }

    private fun resumeDeferredInstall(activity: Activity) {
        val pendingPath = deferredInstallPath ?: return
        deferredInstallPath = null
        val apkFile = try {
            validateAPKFile(pendingPath)
        } catch (_: Exception) {
            return
        }
        val context = PluginHost.context
        if (!canInstallAPK(context)) {
            runCatching { launchUnknownSourcesSettings(activity, context) }
            PluginHost.emit("mgnotedownloads", "installState", JSONObject().put("status", "permission_required"))
            return
        }
        if (runCatching { launchAPKInstaller(activity, context, apkFile) }.isSuccess) {
            PluginHost.emit("mgnotedownloads", "installState", JSONObject().put("status", "launched"))
        }
    }

    private fun launchUnknownSourcesSettings(activity: Activity, context: Context) {
        val settingsIntent = Intent(
            Settings.ACTION_MANAGE_UNKNOWN_APP_SOURCES,
            Uri.parse("package:${context.packageName}")
        ).addFlags(Intent.FLAG_ACTIVITY_NEW_TASK)
        try {
            activity.startActivity(settingsIntent)
        } catch (_: ActivityNotFoundException) {
            val fallback = Intent(Settings.ACTION_MANAGE_UNKNOWN_APP_SOURCES)
                .addFlags(Intent.FLAG_ACTIVITY_NEW_TASK)
            try {
                activity.startActivity(fallback)
            } catch (_: ActivityNotFoundException) {
                throw PluginFailure("SettingsUnavailable", "无法打开未知来源应用安装权限设置")
            }
        }
    }

    private fun launchAPKInstaller(activity: Activity, context: Context, apkFile: File) {
        val authority = "${context.packageName}.mygo.files"
        val contentUri = FileProvider.getUriForFile(context, authority, apkFile)
        val installIntent = Intent(Intent.ACTION_VIEW)
            .setDataAndType(contentUri, "application/vnd.android.package-archive")
            .addFlags(Intent.FLAG_GRANT_READ_URI_PERMISSION or Intent.FLAG_ACTIVITY_NEW_TASK)
        try {
            activity.startActivity(installIntent)
        } catch (_: ActivityNotFoundException) {
            throw PluginFailure("InstallerUnavailable", "系统未找到可用的 APK 安装程序")
        }
    }

    private inner class PdfRendererSlot(private val executor: ExecutorService) {
        private var cachedPath: String? = null
        private var cachedModified: Long = -1L
        private var cachedLength: Long = -1L
        private var renderer: PdfRenderer? = null
        private var idleFuture: ScheduledFuture<*>? = null

        fun hasOpenRenderer(): Boolean = renderer != null

        fun getOrOpen(source: File): PdfRenderer {
            idleFuture?.cancel(false)
            idleFuture = null
            val canonical = runCatching { source.canonicalPath }.getOrElse { source.absolutePath }
            val mtime = source.lastModified()
            val len = source.length()
            val existing = renderer
            if (existing != null && cachedPath == canonical && cachedModified == mtime && cachedLength == len) {
                scheduleIdleClose()
                return existing
            }
            closeSync()
            // Ownership of the seekable descriptor passes to PdfRenderer after construction.
            val fd = ParcelFileDescriptor.open(source, ParcelFileDescriptor.MODE_READ_ONLY)
            val opened = try {
                PdfRenderer(fd)
            } catch (err: Exception) {
                runCatching { fd.close() }
                throw err
            }
            cachedPath = canonical
            cachedModified = mtime
            cachedLength = len
            renderer = opened
            scheduleIdleClose()
            return opened
        }

        fun closeSync() {
            idleFuture?.cancel(false)
            idleFuture = null
            runCatching { renderer?.close() }
            renderer = null
            cachedPath = null
            cachedModified = -1L
            cachedLength = -1L
        }

        fun closeAsync() {
            executor.execute { closeSync() }
        }

        private fun scheduleIdleClose() {
            idleFuture?.cancel(false)
            idleFuture = pdfIdleScheduler.schedule({
                executor.execute { closeSync() }
            }, 60L, TimeUnit.SECONDS)
        }
    }

    private fun renderPDF(args: JSONObject, slot: PdfRendererSlot): JSONObject {
        val source = File(args.getString("path"))
        val target = File(args.getString("output"))
        val index = args.getInt("page")
        val maxSide = args.getInt("maxSide").coerceIn(256, 3000)

        fun renderOnce(): JSONObject {
            val pdf = slot.getOrOpen(source)
            return pdf.openPage(index).use { page ->
                val scale = maxSide.toFloat() / maxOf(page.width, page.height)
                val width = (page.width * scale).roundToInt().coerceAtLeast(1)
                val height = (page.height * scale).roundToInt().coerceAtLeast(1)
                val bitmap = Bitmap.createBitmap(width, height, Bitmap.Config.ARGB_8888)
                try {
                    bitmap.eraseColor(Color.WHITE)
                    val matrix = Matrix().apply { setScale(scale, scale) }
                    page.render(bitmap, null, matrix, PdfRenderer.Page.RENDER_MODE_FOR_DISPLAY)
                    target.outputStream().use {
                        if (!bitmap.compress(Bitmap.CompressFormat.JPEG, 92, it)) {
                            throw IOException("无法生成 PDF 页面图片")
                        }
                    }
                } finally {
                    bitmap.recycle()
                }
                JSONObject().put("count", pdf.pageCount)
                    .put("width", page.width).put("height", page.height)
            }
        }

        val hadCached = slot.hasOpenRenderer()
        return try {
            renderOnce()
        } catch (err: Exception) {
            slot.closeSync()
            if (!hadCached) throw err
            renderOnce()
        }
    }

    private fun extractUris(intent: Intent): List<Uri> = when (intent.action) {
        Intent.ACTION_VIEW -> listOfNotNull(intent.data)
        Intent.ACTION_SEND -> listOfNotNull(
            if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.TIRAMISU) {
                intent.getParcelableExtra(Intent.EXTRA_STREAM, Uri::class.java)
            } else {
                @Suppress("DEPRECATION")
                intent.getParcelableExtra(Intent.EXTRA_STREAM)
            }
        )
        Intent.ACTION_SEND_MULTIPLE -> {
            val list = if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.TIRAMISU) {
                intent.getParcelableArrayListExtra(Intent.EXTRA_STREAM, Uri::class.java)
            } else {
                @Suppress("DEPRECATION")
                intent.getParcelableArrayListExtra(Intent.EXTRA_STREAM)
            }
            list?.filterNotNull().orEmpty()
        }
        else -> emptyList()
    }

    private fun open(intent: Intent) {
        val uris = extractUris(intent).filter {
            it.scheme == ContentResolver.SCHEME_CONTENT || it.scheme == ContentResolver.SCHEME_FILE
        }
        if (uris.isEmpty()) return
        for (uri in uris) {
            worker.execute {
                val result = try {
                    JSONObject().put("path", copyNote(uri).absolutePath)
                } catch (err: IOException) {
                    JSONObject().put("error", err.message ?: "无法读取笔记文件")
                } catch (_: Exception) {
                    JSONObject().put("error", "无法读取笔记文件")
                }
                pending.add(result)
                PluginHost.emit("mgnotedownloads", "ready")
            }
        }
    }

    private fun cleanupStaleTempDirs() {
        val cacheDir = runCatching { PluginHost.context.cacheDir }.getOrNull() ?: return
        val cutoff = System.currentTimeMillis() - TimeUnit.HOURS.toMillis(1)
        cacheDir.listFiles()?.forEach { entry ->
            if (entry.isDirectory && entry.name.startsWith("margin-open-") && entry.lastModified() <= cutoff) {
                runCatching { entry.deleteRecursively() }
            }
        }
    }

    private fun copyNote(uri: Uri): File {
        val context = PluginHost.context
        val resolver = context.contentResolver
        var displayName: String? = null
        var totalSize = -1L
        if (uri.scheme == ContentResolver.SCHEME_CONTENT) {
            runCatching {
                resolver.query(
                    uri,
                    arrayOf(OpenableColumns.DISPLAY_NAME, OpenableColumns.SIZE),
                    null,
                    null,
                    null,
                )?.use {
                    if (it.moveToFirst()) {
                        val nameIdx = it.getColumnIndex(OpenableColumns.DISPLAY_NAME)
                        if (nameIdx >= 0 && !it.isNull(nameIdx)) {
                            displayName = it.getString(nameIdx)
                        }
                        val sizeIdx = it.getColumnIndex(OpenableColumns.SIZE)
                        if (sizeIdx >= 0 && !it.isNull(sizeIdx)) {
                            totalSize = it.getLong(sizeIdx)
                        }
                    }
                }
            }
        } else if (uri.scheme == ContentResolver.SCHEME_FILE) {
            uri.path?.let { p ->
                val f = File(p)
                if (f.isFile) totalSize = f.length()
            }
        }
        val reserveBytes = 64L * 1024L * 1024L
        if (totalSize > 0L && context.cacheDir.usableSpace < totalSize + reserveBytes) {
            throw IOException("设备存储空间不足，无法导入该文件")
        }
        val rawName = (displayName ?: uri.lastPathSegment.orEmpty())
            .substringAfterLast('/')
            .substringAfterLast(':')
            .replace('\\', '_')
            .trim()

        val folder = Files.createTempDirectory(context.cacheDir.toPath(), "margin-open-").toFile()
        val tempFile = File(folder, "incoming.tmp")
        val header = ByteArray(16)
        var headerLen = 0
        var emittedProgress = false
        try {
            val input = resolver.openInputStream(uri) ?: throw IOException("无法打开笔记文件")
            if (totalSize >= 10L * 1024L * 1024L) {
                emittedProgress = true
                PluginHost.emit(
                    "mgnotedownloads",
                    "importProgress",
                    JSONObject().put("copied", 0L).put("total", totalSize),
                )
            }
            var lastEmitMs = System.currentTimeMillis()
            val ext = rawName.substringAfterLast('.', "").lowercase()
            val knownExt = ext == "marginpkg" || ext == "marginnotes" || ext == "pdf"
            var headerChecked = false
            fun resolveFormatFromHeader(): String {
                return when {
                    knownExt -> rawName
                    headerLen >= 4 &&
                        header[0] == 0x50.toByte() && header[1] == 0x4B.toByte() &&
                        header[2] == 0x03.toByte() && header[3] == 0x04.toByte() -> {
                        val base = rawName.ifEmpty { "imported" }
                        "$base.marginpkg"
                    }
                    headerLen >= 16 && String(header, 0, 16, Charsets.US_ASCII) == "SQLite format 3\u0000" -> {
                        val base = rawName.ifEmpty { "imported" }
                        "$base.marginnotes"
                    }
                    headerLen >= 5 && String(header, 0, 5, Charsets.US_ASCII) == "%PDF-" -> {
                        val base = rawName.ifEmpty { "imported" }
                        "$base.pdf"
                    }
                    else -> throw IOException("请选择 .marginpkg、.marginnotes 或 .pdf 文件")
                }
            }
            input.use {
                tempFile.outputStream().use { output ->
                    val buffer = ByteArray(256 * 1024)
                    var size = 0L
                    while (true) {
                        val read = input.read(buffer)
                        if (read < 0) break
                        if (headerLen < header.size) {
                            val take = minOf(read, header.size - headerLen)
                            System.arraycopy(buffer, 0, header, headerLen, take)
                            headerLen += take
                        }
                        if (!headerChecked && !knownExt && headerLen >= header.size) {
                            headerChecked = true
                            resolveFormatFromHeader()
                        }
                        size += read
                        output.write(buffer, 0, read)
                        val now = System.currentTimeMillis()
                        if (now - lastEmitMs >= 200L) {
                            lastEmitMs = now
                            emittedProgress = true
                            PluginHost.emit(
                                "mgnotedownloads",
                                "importProgress",
                                JSONObject().put("copied", size).put("total", totalSize),
                            )
                        }
                    }
                }
            }

            val finalName = resolveFormatFromHeader()

            val target = File(folder, finalName)
            if (!tempFile.renameTo(target)) {
                throw IOException("无法暂存笔记文件")
            }
            return target
        } catch (err: Exception) {
            tempFile.delete()
            folder.deleteRecursively()
            throw err
        } finally {
            if (emittedProgress) {
                PluginHost.emit("mgnotedownloads", "importProgress", JSONObject().put("done", true))
            }
        }
    }
}
