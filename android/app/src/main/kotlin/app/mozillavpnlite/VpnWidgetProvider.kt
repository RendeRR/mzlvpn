package app.mozillavpnlite

import android.app.PendingIntent
import android.appwidget.AppWidgetManager
import android.appwidget.AppWidgetProvider
import android.content.ComponentName
import android.content.Context
import android.content.Intent
import android.widget.RemoteViews

/**
 * Home-screen toggle widget: a bare breaker switch in a 1x1 cell,
 * no text — the pictogram mirrors the app's own breaker (lever down
 * = off, lever up in signal orange = on). State changes are pushed
 * by MinimalVpnService (start, connected, stop, destroy) through
 * [VpnWidgetViews.refresh]; onUpdate only covers placement and
 * launcher-driven refreshes. The whole widget is one button that
 * fires VpnToggleActivity.
 */
class VpnWidgetProvider : AppWidgetProvider() {
    override fun onUpdate(context: Context, manager: AppWidgetManager, ids: IntArray) {
        for (id in ids) {
            val views = try {
                VpnWidgetViews.build(context)
            } catch (_: Throwable) {
                // Never leave the launcher with nothing: the bare
                // layout (static pictogram) is the last-resort frame.
                RemoteViews(context.packageName, R.layout.widget_switch)
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
        val views = RemoteViews(context.packageName, R.layout.widget_switch)
        val on = MinimalVpnService.isRunning || MinimalVpnService.isStarting

        val togglePending = PendingIntent.getActivity(
            context,
            0,
            Intent(context, VpnToggleActivity::class.java),
            PendingIntent.FLAG_UPDATE_CURRENT or PendingIntent.FLAG_IMMUTABLE
        )
        views.setOnClickPendingIntent(R.id.widget_root, togglePending)
        views.setImageViewResource(
            R.id.widget_switch,
            if (on) R.drawable.widget_switch_on else R.drawable.widget_switch_off
        )
        return views
    }
}
