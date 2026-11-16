package com.getan.golder.mobile

import androidx.compose.foundation.Image
import androidx.compose.foundation.gestures.awaitEachGesture
import androidx.compose.foundation.gestures.awaitFirstDown
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.material3.AlertDialog
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.geometry.Offset
import androidx.compose.ui.graphics.asImageBitmap
import androidx.compose.ui.input.pointer.pointerInput
import androidx.compose.ui.layout.ContentScale
import androidx.compose.ui.platform.LocalConfiguration
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.unit.IntSize
import androidx.compose.ui.unit.dp
import com.getan.golder.mobile.androiduse.BackgroundPreview
import com.getan.golder.mobile.androiduse.ManualTakeover
import com.getan.golder.mobile.androiduse.Phase2Session
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.delay
import kotlinx.coroutines.launch
import kotlinx.coroutines.withContext

private data class DisplayPoint(val x: Int, val y: Int, val time: Long)

/** Map the fitted image, not its letterbox, to the actual virtual display. */
private fun displayPoint(
    point: Offset,
    area: IntSize,
    imageWidth: Int,
    imageHeight: Int,
    displayWidth: Int,
    displayHeight: Int,
    clamp: Boolean,
): Pair<Int, Int>? {
    if (area.width <= 0 || area.height <= 0 || imageWidth <= 0 || imageHeight <= 0 ||
        displayWidth <= 0 || displayHeight <= 0
    ) return null
    val scale = minOf(area.width.toFloat() / imageWidth, area.height.toFloat() / imageHeight)
    val width = imageWidth * scale
    val height = imageHeight * scale
    val left = (area.width - width) / 2f
    val top = (area.height - height) / 2f
    val x = (point.x - left) / width
    val y = (point.y - top) / height
    if (!clamp && (x < 0f || x >= 1f || y < 0f || y >= 1f)) return null
    return (x.coerceIn(0f, 1f) * displayWidth).toInt().coerceIn(0, displayWidth - 1) to
        (y.coerceIn(0f, 1f) * displayHeight).toInt().coerceIn(0, displayHeight - 1)
}

@Composable
internal fun BackgroundPreviewDialog(
    preview: BackgroundPreview.State?,
    chinese: Boolean,
    handle: Int,
    allowTakeover: Boolean,
    takeover: ManualTakeover.State,
    onClose: () -> Unit,
) {
    val context = LocalContext.current
    val scope = rememberCoroutineScope()
    val height = (LocalConfiguration.current.screenHeightDp - 310).coerceIn(120, 460).dp
    var inputBusy by remember(preview?.displayId) { mutableStateOf(false) }
    val active = takeover.stage == ManualTakeover.Stage.ACTIVE && preview?.displayId == takeover.displayId
    val canClose = takeover.stage == ManualTakeover.Stage.IDLE
    val frame = preview?.frame

    AlertDialog(
        modifier = Modifier.testTag("background-preview-dialog"),
        onDismissRequest = { if (canClose) onClose() },
        title = { Text(if (chinese) "后台实时画面" else "Live background display") },
        text = {
            Column {
                when (takeover.stage) {
                    ManualTakeover.Stage.IDLE -> Text(if (chinese) "接管后可在画面上点击、滑动和输入。" else "Take control to tap, swipe, and use the on-screen keyboard.")
                    ManualTakeover.Stage.WAITING -> Text(if (chinese) "正在等待 Agent 当前操作完成…" else "Waiting for the agent's current action to finish…")
                    ManualTakeover.Stage.ACTIVE -> Text(if (chinese) "你正在操作；Agent 的工具调用已暂停，正在生成的回复可能继续。交还后 Agent 可查看画面。" else "You are in control; agent tool calls are paused, though an in-flight reply may continue. It can view the screen after handback.")
                    ManualTakeover.Stage.RETURNING -> Text(if (chinese) "正在交还控制权…" else "Handing control back…")
                }
                if (frame == null) {
                    Text(if (chinese) "画面不可用。" else "Display unavailable.")
                } else {
                    Image(
                        bitmap = frame.asImageBitmap(),
                        contentDescription = if (chinese) "可操作的后台画面" else "Interactive background display",
                        contentScale = ContentScale.Fit,
                        modifier = Modifier.fillMaxWidth().height(height)
                            .testTag("background-preview-image")
                            .pointerInput(active, inputBusy, preview.displayId, frame.width, frame.height) {
                                if (!active || inputBusy) return@pointerInput
                                awaitEachGesture {
                                    val down = awaitFirstDown(requireUnconsumed = false)
                                    val first = displayPoint(down.position, size, frame.width, frame.height, preview.width, preview.height, false)
                                        ?: return@awaitEachGesture
                                    val points = ArrayList<DisplayPoint>()
                                    points.add(DisplayPoint(first.first, first.second, 0))
                                    val start = down.uptimeMillis
                                    var last = down
                                    var multiTouch = false
                                    down.consume()
                                    do {
                                        val event = awaitPointerEvent()
                                        if (event.changes.any { it.id != down.id && it.pressed }) multiTouch = true
                                        val change = event.changes.firstOrNull { it.id == down.id } ?: break
                                        last = change
                                        val mapped = displayPoint(change.position, size, frame.width, frame.height, preview.width, preview.height, true)
                                        val elapsed = (change.uptimeMillis - start).coerceIn(0, 5_000)
                                        if (mapped != null && points.size < 255 && change.pressed && elapsed - points.last().time >= 16) {
                                            points.add(DisplayPoint(mapped.first, mapped.second, elapsed))
                                        }
                                        change.consume()
                                    } while (last.pressed)
                                    if (!multiTouch && !last.pressed) {
                                        val end = displayPoint(last.position, size, frame.width, frame.height, preview.width, preview.height, true)
                                        if (end != null) {
                                            val duration = (last.uptimeMillis - start).coerceIn(50, 5_000)
                                                .coerceAtLeast(points.last().time)
                                            points.add(DisplayPoint(end.first, end.second, duration))
                                            inputBusy = true
                                            scope.launch {
                                                val ok = withContext(Dispatchers.IO) {
                                                    Phase2Session.manualStroke(
                                                        context, preview.displayId,
                                                        points.map { it.x }.toIntArray(),
                                                        points.map { it.y }.toIntArray(),
                                                        points.map { it.time }.toLongArray(),
                                                    )
                                                }
                                                if (ok) delay(250) // Allow the preview to catch up before handback.
                                                inputBusy = false
                                                if (!ok) ManualTakeover.inputFailed()
                                            }
                                        }
                                    }
                                }
                            },
                    )
                }
                if (takeover.error.isNotBlank()) {
                    Text(takeover.error, color = MaterialTheme.colorScheme.error, style = MaterialTheme.typography.labelSmall)
                }
            }
        },
        confirmButton = {
            when (takeover.stage) {
                ManualTakeover.Stage.IDLE -> TextButton(
                    enabled = frame != null && allowTakeover,
                    onClick = { preview?.let { ManualTakeover.request(context, handle, it.displayId) } },
                ) { Text(if (chinese) "接管操作" else "Take over") }
                ManualTakeover.Stage.WAITING -> TextButton(onClick = ManualTakeover::cancelWaiting) { Text(if (chinese) "取消" else "Cancel") }
                ManualTakeover.Stage.ACTIVE -> TextButton(enabled = !inputBusy, onClick = ManualTakeover::handBack) { Text(if (chinese) "交还 Agent" else "Hand back") }
                ManualTakeover.Stage.RETURNING -> TextButton(enabled = false, onClick = {}) { Text(if (chinese) "交还中" else "Returning") }
            }
        },
        dismissButton = {
            when (takeover.stage) {
                // Collapsing leaves the background display running; closing it is
                // a separate, explicit choice.
                ManualTakeover.Stage.IDLE -> Row {
                    TextButton(onClick = {
                        Phase2Session.release(context)
                        onClose()
                    }) { Text(if (chinese) "关闭后台显示" else "Close display") }
                    TextButton(onClick = onClose) { Text(if (chinese) "收起" else "Collapse") }
                }
                ManualTakeover.Stage.ACTIVE -> Row {
                    TextButton(enabled = active && !inputBusy, onClick = {
                        val id = preview?.displayId ?: return@TextButton
                        inputBusy = true
                        scope.launch {
                            val ok = withContext(Dispatchers.IO) { Phase2Session.manualBack(context, id) }
                            if (ok) delay(250)
                            inputBusy = false
                            if (!ok) ManualTakeover.inputFailed()
                        }
                    }) { Text(if (chinese) "返回" else "Back") }
                }
                else -> Unit
            }
        },
    )
}
