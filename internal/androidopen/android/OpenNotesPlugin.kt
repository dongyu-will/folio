package dev.mygo

import android.content.ActivityNotFoundException
import android.content.ContentResolver
import android.content.Intent
import android.graphics.Bitmap
import android.graphics.Color
import android.graphics.Matrix
import android.graphics.pdf.PdfRenderer
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
import java.util.concurrent.Executors
import kotlin.math.roundToInt
import org.json.JSONObject

class OpenNotesPlugin : MyGoPlugin {
    private val stylus = StylusBridge()
    private val pending = ConcurrentLinkedQueue<JSONObject>()
    private val worker = Executors.newSingleThreadExecutor()
    private val pdfForeground = Executors.newSingleThreadExecutor()
    private val pdfBackground = Executors.newSingleThreadExecutor()

    init {
        PluginHost.onIntent = ::open
        LaunchCover.install(PluginHost.context, PluginHost.activity)
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
                val renderer = if (args.optBoolean("background")) pdfBackground else pdfForeground
                renderer.execute {
                    try {
                        reply.success(renderPDF(args))
                    } catch (err: Exception) {
                        reply.success(JSONObject().put("error", err.localizedMessage ?: "无法渲染 PDF"))
                    }
                }
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

    private fun installAPK(args: JSONObject): JSONObject {
        val rawPath = args.optString("path", "").trim()
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

        val context = PluginHost.context
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.O &&
            !context.packageManager.canRequestPackageInstalls()
        ) {
            val settingsIntent = Intent(
                Settings.ACTION_MANAGE_UNKNOWN_APP_SOURCES,
                Uri.parse("package:${context.packageName}")
            ).addFlags(Intent.FLAG_ACTIVITY_NEW_TASK)
            try {
                (PluginHost.activity ?: context).startActivity(settingsIntent)
            } catch (_: ActivityNotFoundException) {
                val fallback = Intent(Settings.ACTION_MANAGE_UNKNOWN_APP_SOURCES)
                    .addFlags(Intent.FLAG_ACTIVITY_NEW_TASK)
                try {
                    (PluginHost.activity ?: context).startActivity(fallback)
                } catch (_: ActivityNotFoundException) {
                    throw PluginFailure("SettingsUnavailable", "无法打开未知来源应用安装权限设置")
                }
            }
            return JSONObject().put("status", "permission_required")
        }

        val authority = "${context.packageName}.mygo.files"
        val contentUri = FileProvider.getUriForFile(context, authority, apkFile)
        val installIntent = Intent(Intent.ACTION_VIEW)
            .setDataAndType(contentUri, "application/vnd.android.package-archive")
            .addFlags(Intent.FLAG_GRANT_READ_URI_PERMISSION or Intent.FLAG_ACTIVITY_NEW_TASK)
        try {
            (PluginHost.activity ?: context).startActivity(installIntent)
        } catch (_: ActivityNotFoundException) {
            throw PluginFailure("InstallerUnavailable", "系统未找到可用的 APK 安装程序")
        }
        return JSONObject().put("status", "launched")
    }

    private fun renderPDF(args: JSONObject): JSONObject {
        val source = File(args.getString("path"))
        val target = File(args.getString("output"))
        val index = args.getInt("page")
        val maxSide = args.getInt("maxSide").coerceIn(256, 3000)
        // Ownership of the seekable descriptor passes to PdfRenderer after construction.
        val fd = ParcelFileDescriptor.open(source, ParcelFileDescriptor.MODE_READ_ONLY)
        val renderer = try {
            PdfRenderer(fd)
        } catch (err: Exception) {
            fd.close()
            throw err
        }
        renderer.use { pdf ->
            pdf.openPage(index).use { page ->
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
            }
            return JSONObject().put("count", pdf.pageCount)
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

    private fun copyNote(uri: Uri): File {
        val context = PluginHost.context
        val resolver = context.contentResolver
        val displayName = if (uri.scheme == ContentResolver.SCHEME_CONTENT) {
            runCatching {
                resolver.query(uri, arrayOf(OpenableColumns.DISPLAY_NAME), null, null, null)?.use {
                    if (it.moveToFirst()) it.getString(0) else null
                }
            }.getOrNull()
        } else {
            null
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
        try {
            val input = resolver.openInputStream(uri) ?: throw IOException("无法打开笔记文件")
            input.use {
                tempFile.outputStream().use { output ->
                    val buffer = ByteArray(64 * 1024)
                    var size = 0L
                    while (true) {
                        val read = input.read(buffer)
                        if (read < 0) break
                        if (headerLen < header.size) {
                            val take = minOf(read, header.size - headerLen)
                            System.arraycopy(buffer, 0, header, headerLen, take)
                            headerLen += take
                        }
                        size += read
                        if (size > 512L * 1024 * 1024) throw IOException("笔记文件超过 512 MB")
                        output.write(buffer, 0, read)
                    }
                }
            }

            val ext = rawName.substringAfterLast('.', "").lowercase()
            val finalName = when {
                ext == "marginpkg" || ext == "marginnotes" || ext == "pdf" -> rawName
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

            val target = File(folder, finalName)
            if (!tempFile.renameTo(target)) {
                throw IOException("无法暂存笔记文件")
            }
            return target
        } catch (err: Exception) {
            tempFile.delete()
            folder.deleteRecursively()
            throw err
        }
    }
}
