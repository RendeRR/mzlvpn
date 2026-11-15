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
 * Home-screen toggle widget. State changes are pushed by
 * MinimalVpnService (start, connected, stop, destroy) through
 * [VpnWidgetViews.refresh]; onUpdate only covers placement and
 * launcher-driven refreshes. The whole widget is one button that
 * fires VpnToggleActivity.
 */
class VpnWidgetProvider : AppWidgetProvider() {
    override fun onUpdate(context: Context, manager: AppWidgetManager, ids: IntArray) {
        // DIAGNOSTIC (2026-11-15): the full «Прибор» widget shows
        // "Can't load widget" on the user's device in two different
        // launchers, although the shipped APK's manifest, provider
        // info and compiled layout all verify clean with aapt2.
        // Serve the bare-minimum diagnostic layout (one TextView,
        // zero resource references) to bisect the failure: if this
        // renders, the fancy layout or one of its resources is the
        // culprit; if it still fails, the fault is in provider
        // delivery/registration itself. VpnWidgetViews (the service's
        // state pushes) is intentionally bypassed here.
        val views = RemoteViews(context.packageName, R.layout.widget_diag)
        for (id in ids) {
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

        when {
            running -> {
                views.setInt(
                    R.id.widget_square,
                    "setBackgroundResource",
                    R.drawable.widget_square_signal
                )
                val (state, detail) = connectedTexts(context)
                views.setTextViewText(R.id.widget_state, state)
                views.setTextViewText(R.id.widget_detail, detail)
                views.setTextViewText(
                    R.id.widget_action,
                    context.getString(R.string.widget_action_disconnect)
                )
                views.setInt(
                    R.id.widget_action,
                    "setBackgroundResource",
                    R.drawable.widget_btn_ink
                )
                views.setTextColor(R.id.widget_action, paper)
            }
            starting -> {
                views.setInt(
                    R.id.widget_square,
                    "setBackgroundResource",
                    R.drawable.widget_square_signal
                )
                views.setTextViewText(
                    R.id.widget_state,
                    context.getString(R.string.widget_state_connecting)
                )
                views.setTextViewText(R.id.widget_detail, "")
                views.setTextViewText(
                    R.id.widget_action,
                    context.getString(R.string.widget_state_connecting)
                )
                views.setInt(
                    R.id.widget_action,
                    "setBackgroundResource",
                    R.drawable.widget_btn_ink
                )
                views.setTextColor(R.id.widget_action, paper)
            }
            else -> {
                views.setInt(
                    R.id.widget_square,
                    "setBackgroundResource",
                    R.drawable.widget_square_ink
                )
                views.setTextViewText(
                    R.id.widget_state,
                    context.getString(R.string.state_off)
                )
                views.setTextViewText(
                    R.id.widget_detail,
                    context.getString(R.string.widget_hint_tap_on)
                )
                views.setTextViewText(
                    R.id.widget_action,
                    context.getString(R.string.widget_action_connect)
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

    /** Localized "country" headline + "email · host" detail, mirroring
     *  the foreground notification. Runs only while connected, i.e. in
     *  the service's process where the core is started. */
    private fun connectedTexts(context: Context): Pair<String, String> {
        return try {
            val root = JSONObject(mobile.Mobile.statusJSON())
            val loc = root.optJSONObject("location")
            val isRu = context.resources.configuration.locales[0]?.language == "ru"
            val country = if (isRu) {
                loc?.optString("name_ru", "")?.ifBlank { null }
                    ?: loc?.optString("name", "") ?: ""
            } else {
                loc?.optString("name", "") ?: ""
            }
            val code = loc?.optString("code", "") ?: ""
            val host = loc?.optString("server_host", "") ?: ""
            var email = ""
            val accs = root.optJSONArray("accounts")
            if (accs != null) {
                for (i in 0 until accs.length()) {
                    val o = accs.optJSONObject(i) ?: continue
                    if (o.optBoolean("active", false)) {
                        email = o.optString("email", "")
                        break
                    }
                }
            }
            val state = when {
                country.isNotBlank() && code.isNotBlank() -> "$code · $country"
                country.isNotBlank() -> country
                else -> context.getString(R.string.state_on)
            }
            val detail = listOf(email, host).filter { it.isNotBlank() }.joinToString(" · ")
            state to detail
        } catch (_: Throwable) {
            context.getString(R.string.state_on) to ""
        }
    }
}
