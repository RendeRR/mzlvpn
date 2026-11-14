package app.mozillavpnlite

import android.app.Activity
import android.content.Intent
import android.net.VpnService
import android.os.Bundle
import androidx.activity.ComponentActivity
import androidx.activity.compose.rememberLauncherForActivityResult
import androidx.activity.compose.setContent
import androidx.activity.result.contract.ActivityResultContracts
import androidx.compose.foundation.background
import androidx.compose.foundation.border
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.*
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material3.*
import androidx.compose.runtime.*
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.delay
import kotlinx.coroutines.launch
import kotlinx.coroutines.withContext

class MainActivity : ComponentActivity() {
    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        MinimalVpnService.startCore(this, on = false)

        setContent {
            MaterialTheme(
                colorScheme = darkColorScheme(
                    primary = Color(0xFF6366F1),
                    surface = Color(0xFF0F172A),
                    background = Color(0xFF020617)
                )
            ) {
                MinimalVpnApp(activity = this)
            }
        }
    }
}

@Composable
fun MinimalVpnApp(activity: ComponentActivity) {
    val scope = rememberCoroutineScope()
    var tab by remember { mutableIntStateOf(0) } // 0 = Пульт, 1 = Аккаунты, 2 = Серверы
    var state by remember { mutableStateOf(CoreBridge.fetchSnapshot()) }

    val vpnPermissionLauncher = rememberLauncherForActivityResult(
        ActivityResultContracts.StartActivityForResult()
    ) { res ->
        if (res.resultCode == Activity.RESULT_OK) {
            MinimalVpnService.turnOn(activity)
            state = CoreBridge.fetchSnapshot()
        }
    }

    LaunchedEffect(Unit) {
        while (true) {
            state = withContext(Dispatchers.IO) { CoreBridge.fetchSnapshot() }
            delay(1500)
        }
    }

    Scaffold(
        containerColor = Color(0xFF020617),
        bottomBar = {
            NavigationBar(containerColor = Color(0xFF0F172A)) {
                NavigationBarItem(
                    selected = tab == 0,
                    onClick = { tab = 0 },
                    icon = {},
                    label = { Text("Пульт", fontWeight = FontWeight.SemiBold) }
                )
                NavigationBarItem(
                    selected = tab == 1,
                    onClick = { tab = 1 },
                    icon = {},
                    label = { Text("Аккаунты  (${state.accounts.size})", fontWeight = FontWeight.SemiBold) }
                )
                NavigationBarItem(
                    selected = tab == 2,
                    onClick = { tab = 2 },
                    icon = {},
                    label = { Text("Серверы  (${state.locations.size})", fontWeight = FontWeight.SemiBold) }
                )
            }
        }
    ) { padding ->
        Box(modifier = Modifier.padding(padding).fillMaxSize()) {
            when (tab) {
                0 -> QuickSwitchHome(
                    state = state,
                    onToggleVpn = {
                        if (state.connected) {
                            MinimalVpnService.turnOff(activity)
                        } else {
                            val prep: Intent? = VpnService.prepare(activity)
                            if (prep != null) vpnPermissionLauncher.launch(prep)
                            else MinimalVpnService.turnOn(activity)
                        }
                        state = CoreBridge.fetchSnapshot()
                    },
                    onSelectAccount = { id ->
                        scope.launch(Dispatchers.IO) {
                            try { mobile.Mobile.switchAccount(id) } catch (_: Throwable) {}
                            val next = CoreBridge.fetchSnapshot()
                            withContext(Dispatchers.Main) { state = next }
                        }
                    },
                    onSelectLocation = { code ->
                        scope.launch(Dispatchers.IO) {
                            try { mobile.Mobile.setLocation(code) } catch (_: Throwable) {}
                            val next = CoreBridge.fetchSnapshot()
                            withContext(Dispatchers.Main) { state = next }
                        }
                    }
                )
                1 -> AccountsScreen(
                    state = state,
                    onActivate = { id ->
                        scope.launch(Dispatchers.IO) {
                            try { mobile.Mobile.switchAccount(id) } catch (_: Throwable) {}
                            val next = CoreBridge.fetchSnapshot()
                            withContext(Dispatchers.Main) { state = next }
                        }
                    },
                    onAddAccount = { email, pass ->
                        scope.launch(Dispatchers.IO) {
                            try { mobile.Mobile.addAccountPassword(email, pass) } catch (_: Throwable) {}
                            val next = CoreBridge.fetchSnapshot()
                            withContext(Dispatchers.Main) { state = next }
                        }
                    }
                )
                2 -> ServersScreen(
                    state = state,
                    onSelect = { code ->
                        scope.launch(Dispatchers.IO) {
                            try { mobile.Mobile.setLocation(code) } catch (_: Throwable) {}
                            val next = CoreBridge.fetchSnapshot()
                            withContext(Dispatchers.Main) {
                                state = next
                                tab = 0
                            }
                        }
                    },
                    onPingAll = {
                        scope.launch(Dispatchers.IO) {
                            try { mobile.Mobile.pingAllLocations() } catch (_: Throwable) {}
                            val next = CoreBridge.fetchSnapshot()
                            withContext(Dispatchers.Main) { state = next }
                        }
                    }
                )
            }
        }
    }
}

@Composable
fun QuickSwitchHome(
    state: CoreSnapshot,
    onToggleVpn: () -> Unit,
    onSelectAccount: (String) -> Unit,
    onSelectLocation: (String) -> Unit
) {
    LazyColumn(
        modifier = Modifier.fillMaxSize().padding(16.dp),
        verticalArrangement = Arrangement.spacedBy(12.dp)
    ) {
        item {
            Box(
                modifier = Modifier
                    .fillMaxWidth()
                    .clip(RoundedCornerShape(20.dp))
                    .background(if (state.connected) Color(0xFF1E1B4B) else Color(0xFF0F172A))
                    .border(
                        1.dp,
                        if (state.connected) Color(0xFF6366F1) else Color(0xFF1E293B),
                        RoundedCornerShape(20.dp)
                    )
                    .clickable { onToggleVpn() }
                    .padding(20.dp)
            ) {
                Column(verticalArrangement = Arrangement.spacedBy(6.dp)) {
                    Text(
                        text = if (state.connected) "ЗАЩИЩЕНО · HTTP/2 TLS 1.3" else "ОТКЛЮЧЕНО",
                        color = if (state.connected) Color(0xFF34D399) else Color(0xFF94A3B8),
                        fontSize = 12.sp,
                        fontWeight = FontWeight.Bold
                    )
                    Text(
                        text = state.activeLocationName,
                        color = Color.White,
                        fontSize = 22.sp,
                        fontWeight = FontWeight.Bold
                    )
                    Text(
                        text = state.activeServerHost,
                        color = Color(0xFF94A3B8),
                        fontSize = 12.sp
                    )
                }
            }
        }

        items(state.accounts, key = { it.id }) { acc ->
            Box(
                modifier = Modifier
                    .fillMaxWidth()
                    .clip(RoundedCornerShape(16.dp))
                    .background(if (acc.active) Color(0xFF1E1B4B) else Color(0xFF0F172A))
                    .border(
                        1.dp,
                        if (acc.active) Color(0xFF6366F1) else Color(0xFF1E293B),
                        RoundedCornerShape(16.dp)
                    )
                    .clickable { onSelectAccount(acc.id) }
                    .padding(14.dp)
            ) {
                Row(
                    modifier = Modifier.fillMaxWidth(),
                    horizontalArrangement = Arrangement.SpaceBetween,
                    verticalAlignment = Alignment.CenterVertically
                ) {
                    Column {
                        Text(
                            text = "#${acc.position}  ${acc.email}",
                            color = Color.White,
                            fontSize = 14.sp,
                            fontWeight = FontWeight.SemiBold
                        )
                        Text(
                            text = "Осталось: ${acc.remainingGb} из 5.00 ГБ (${acc.usedPercent}%)",
                            color = Color(0xFF94A3B8),
                            fontSize = 12.sp
                        )
                    }
                    Text(
                        text = if (acc.active) "Активен" else "Готов",
                        color = if (acc.active) Color(0xFF818CF8) else Color(0xFF64748B),
                        fontSize = 12.sp,
                        fontWeight = FontWeight.SemiBold
                    )
                }
            }
        }

        items(state.locations.take(6), key = { it.code }) { loc ->
            val selected = state.selectedLocation.equals(loc.code, ignoreCase = true)
            Box(
                modifier = Modifier
                    .fillMaxWidth()
                    .clip(RoundedCornerShape(14.dp))
                    .background(if (selected) Color(0xFF1E1B4B) else Color(0xFF0F172A))
                    .clickable { onSelectLocation(loc.code) }
                    .padding(14.dp)
            ) {
                Row(
                    modifier = Modifier.fillMaxWidth(),
                    horizontalArrangement = Arrangement.SpaceBetween
                ) {
                    Text(text = "${loc.name} (${loc.code})", color = Color.White, fontSize = 14.sp)
                    Text(
                        text = if (loc.pingMs > 0) "${loc.pingMs} мс" else "—",
                        color = Color(0xFF94A3B8),
                        fontSize = 12.sp
                    )
                }
            }
        }
    }
}

@Composable
fun AccountsScreen(
    state: CoreSnapshot,
    onActivate: (String) -> Unit,
    onAddAccount: (String, String) -> Unit
) {
    var email by remember { mutableStateOf("") }
    var password by remember { mutableStateOf("") }

    LazyColumn(
        modifier = Modifier.fillMaxSize().padding(16.dp),
        verticalArrangement = Arrangement.spacedBy(12.dp)
    ) {
        item {
            Column(verticalArrangement = Arrangement.spacedBy(8.dp)) {
                OutlinedTextField(
                    value = email,
                    onValueChange = { email = it },
                    label = { Text("Email аккаунта Mozilla") },
                    modifier = Modifier.fillMaxWidth()
                )
                OutlinedTextField(
                    value = password,
                    onValueChange = { password = it },
                    label = { Text("Пароль (PBKDF2 + HKDF)") },
                    modifier = Modifier.fillMaxWidth()
                )
                Button(
                    onClick = {
                        if (email.isNotBlank() && password.isNotBlank()) {
                            onAddAccount(email.trim(), password)
                            email = ""
                            password = ""
                        }
                    },
                    modifier = Modifier.fillMaxWidth()
                ) {
                    Text("Добавить аккаунт Mozilla")
                }
            }
        }

        items(state.accounts, key = { it.id }) { acc ->
            Box(
                modifier = Modifier
                    .fillMaxWidth()
                    .clip(RoundedCornerShape(16.dp))
                    .background(Color(0xFF0F172A))
                    .clickable { onActivate(acc.id) }
                    .padding(14.dp)
            ) {
                Text(
                    text = "#${acc.position} ${acc.email} · ${acc.remainingGb}",
                    color = Color.White,
                    fontSize = 14.sp
                )
            }
        }
    }
}

@Composable
fun ServersScreen(
    state: CoreSnapshot,
    onSelect: (String) -> Unit,
    onPingAll: () -> Unit
) {
    LazyColumn(
        modifier = Modifier.fillMaxSize().padding(16.dp),
        verticalArrangement = Arrangement.spacedBy(10.dp)
    ) {
        item {
            Button(onClick = onPingAll, modifier = Modifier.fillMaxWidth()) {
                Text("Измерить реальный пинг (HTTP/2 CONNECT)")
            }
        }
        items(state.locations, key = { it.code }) { loc ->
            Box(
                modifier = Modifier
                    .fillMaxWidth()
                    .clip(RoundedCornerShape(14.dp))
                    .background(Color(0xFF0F172A))
                    .clickable { onSelect(loc.code) }
                    .padding(14.dp)
            ) {
                Row(
                    modifier = Modifier.fillMaxWidth(),
                    horizontalArrangement = Arrangement.SpaceBetween
                ) {
                    Text(text = "${loc.name} · ${loc.city}", color = Color.White, fontSize = 14.sp)
                    Text(
                        text = if (loc.pingMs > 0) "${loc.pingMs} мс" else "—",
                        color = Color(0xFF34D399),
                        fontSize = 12.sp
                    )
                }
            }
        }
    }
}
