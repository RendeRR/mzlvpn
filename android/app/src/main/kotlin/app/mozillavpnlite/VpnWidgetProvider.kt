package app.mozillavpnlite

import android.app.PendingIntent
import android.appwidget.AppWidgetManager
import android.appwidget.AppWidgetProvider
import android.content.ComponentName
import android.content.Context
import android.content.Intent
import android.widget.RemoteViews
import org.json.JSONObject

/**
 * Home-screen toggle widget — one line: status square + state text,
 * and the short ВКЛ/ВЫКЛ button on the right. State changes are
 * pushed by MinimalVpnService (start, connected, stop, destroy)
 * through [VpnWidgetViews.refresh]; onUpdate only covers placement
 * and launcher-driven refreshes. The whole widget is one button that
 * fires VpnToggleActivity.
 */
class VpnWidgetProvider : AppWidgetProvider() {
    override fun onUpdate(context: Context, manager: AppWidgetManager, ids: IntArray) {
        for (id in ids) {
            val views = try {
                VpnWidgetViews.build(context)
            } catch (_: Throwable) {
                // Never leave the launcher with nothing: the bare
                // layout (static texts) is the last-resort frame.
                RemoteViews(context.packageName, R.layout.widget_toggle)
            }
            manager.updateAppWidget(id, views)
        }
    }
}

object VpnWidgetViews {

    fun refresh(context: Context) {
        try {
            val manager = AppWidgetManager.getInstance(context) ?: return
            val ids = manager.getAppWidgetIds(
                ComponentName(context, VpnWidgetProvider::class.java)
            )
            if (ids.isEmpty()) return
            val views = build(context)
            for (id in ids) {
                manager.updateAppWidget(id, views)
            }
        } catch (_: Throwable) {}
    }

    fun build(context: Context): RemoteViews {
        val views = RemoteViews(context.packageName, R.layout.widget_toggle)
        val running = MinimalVpnService.isRunning
        val starting = !running && MinimalVpnService.isStarting

        val togglePending = PendingIntent.getActivity(
            context,
            0,
            Intent(context, VpnToggleActivity::class.java),
            PendingIntent.FLAG_UPDATE_CURRENT or PendingIntent.FLAG_IMMUTABLE
        )
        views.setOnClickPendingIntent(R.id.widget_root, togglePending)
        views.setOnClickPendingIntent(R.id.widget_action, togglePending)

        val paper = context.getColor(R.color.pribor_paper)
        val ink = context.getColor(R.color.pribor_ink)
        val signal = context.getColor(R.color.pribor_signal)

        when {
            running -> {
                views.setTextColor(R.id.widget_square, signal)
                views.setTextViewText(R.id.widget_state, connectedState(context))
                views.setTextViewText(
                    R.id.widget_action,
                    context.getString(R.string.widget_btn_off)
                )
                views.setInt(
                    R.id.widget_action,
                    "setBackgroundResource",
                    R.drawable.widget_btn_ink
                )
                views.setTextColor(R.id.widget_action, paper)
            }
            starting -> {
                views.setTextColor(R.id.widget_square, signal)
                views.setTextViewText(
                    R.id.widget_state,
                    context.getString(R.string.widget_state_connecting)
                )
                views.setTextViewText(
                    R.id.widget_action,
                    context.getString(R.string.widget_btn_off)
                )
                views.setInt(
                    R.id.widget_action,
                    "setBackgroundResource",
                    R.drawable.widget_btn_ink
                )
                views.setTextColor(R.id.widget_action, paper)
            }
            else -> {
                views.setTextColor(R.id.widget_square, ink)
                views.setTextViewText(
                    R.id.widget_state,
                    context.getString(R.string.state_off)
                )
                views.setTextViewText(
                    R.id.widget_action,
                    context.getString(R.string.widget_btn_on)
                )
                views.setInt(
                    R.id.widget_action,
                    "setBackgroundResource",
                    R.drawable.widget_btn_signal
                )
                views.setTextColor(R.id.widget_action, paper)
            }
        }
        return views
    }

    /** State line while connected: the country CODE only ("DE") —
     *  the user asked for the code without the country name. Runs
     *  only in that state, i.e. in the service's process where the
     *  core is started. */
    private fun connectedState(context: Context): String {
        return try {
            val root = JSONObject(mobile.Mobile.statusJSON())
            val code = root.optJSONObject("location")?.optString("code", "") ?: ""
            code.ifBlank { context.getString(R.string.state_on) }
        } catch (_: Throwable) {
            context.getString(R.string.state_on)
        }
    }
}
