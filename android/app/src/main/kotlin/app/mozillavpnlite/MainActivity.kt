package app.mozillavpnlite

import android.annotation.SuppressLint
import android.app.Activity
import android.net.VpnService
import android.os.Bundle
import android.webkit.CookieManager
import android.webkit.JavascriptInterface
import android.webkit.WebResourceRequest
import android.webkit.WebView
import android.webkit.WebViewClient
import androidx.activity.ComponentActivity
import androidx.activity.compose.rememberLauncherForActivityResult
import androidx.activity.compose.setContent
import androidx.activity.enableEdgeToEdge
import androidx.activity.result.contract.ActivityResultContracts
import androidx.compose.animation.animateColorAsState
import androidx.compose.foundation.BorderStroke
import androidx.compose.foundation.Canvas
import androidx.compose.foundation.background
import androidx.compose.foundation.border
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.*
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import androidx.compose.foundation.lazy.itemsIndexed
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.foundation.text.KeyboardOptions
import androidx.compose.material3.*
import androidx.compose.runtime.*
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.geometry.Offset
import androidx.compose.ui.geometry.Size
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.StrokeCap
import androidx.compose.ui.graphics.drawscope.Stroke
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.text.font.FontFamily
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.input.ImeAction
import androidx.compose.ui.text.input.KeyboardType
import androidx.compose.ui.text.input.PasswordVisualTransformation
import androidx.compose.ui.text.input.VisualTransformation
import androidx.compose.ui.text.style.TextAlign
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.Dp
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import androidx.compose.ui.viewinterop.AndroidView
import androidx.compose.ui.window.Dialog
import androidx.compose.ui.window.DialogProperties
import kotlinx.coroutines.delay
import kotlinx.coroutines.launch
import kotlin.math.roundToInt

class MainActivity : ComponentActivity() {
    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        enableEdgeToEdge()
        CoreBridge.init(this)

        setContent {
            MaterialTheme(
                colorScheme = darkColorScheme(
                    primary = Color(0xFF6366F1),
                    background = Color(0xFF090D16),
                    surface = Color(0xFF0F172A),
                    onBackground = Color(0xFFF1F5F9),
                    onSurface = Color(0xFFF1F5F9)
                )
            ) {
                Surface(
                    modifier = Modifier.fillMaxSize(),
                    color = Color(0xFF090D16)
                ) {
                    MozillaVpnLiteApp()
                }
            }
        }
    }
}

@Composable
fun MozillaVpnLiteApp() {
    val context = LocalContext.current
    val scope = rememberCoroutineScope()

    var snap by remember { mutableStateOf(CoreBridge.getSnapshot()) }
    var tab by remember { mutableIntStateOf(0) } // 0 = Пульт, 1 = Аккаунты, 2 = Серверы
    var isConnecting by remember { mutableStateOf(false) }
    var isPinging by remember { mutableStateOf(false) }
    var searchQuery by remember { mutableStateOf("") }

    // Account sign-in states
    var showAddForm by remember { mutableStateOf(false) }
    var authMethod by remember { mutableIntStateOf(0) } // 0 = Прямой вход, 1 = Браузер Mozilla, 2 = Токен 64-hex
    var emailInput by remember { mutableStateOf("") }
    var passwordInput by remember { mutableStateOf("") }
    var showPassword by remember { mutableStateOf(false) }
    var unblockCodeInput by remember { mutableStateOf("") }
    var verifyCodeInput by remember { mutableStateOf("") }
    var sessionTokenInput by remember { mutableStateOf("") }
    var authStepState by remember { mutableStateOf("idle") } // idle | verify_email | verify_totp | unblock_code
    var authMessage by remember { mutableStateOf<String?>(null) }
    var authIsError by remember { mutableStateOf(false) }
    var isSigningIn by remember { mutableStateOf(false) }
    var showWebViewDialog by remember { mutableStateOf(false) }

    val vpnPermissionLauncher = rememberLauncherForActivityResult(
        ActivityResultContracts.StartActivityForResult()
    ) { res ->
        if (res.resultCode == Activity.RESULT_OK) {
            scope.launch {
                isConnecting = true
                MinimalVpnService.turnOn(context)
                delay(450)
                snap = CoreBridge.getSnapshot()
                isConnecting = false
            }
        }
    }

    LaunchedEffect(Unit) {
        CoreBridge.refreshServers()
        snap = CoreBridge.getSnapshot()
        while (true) {
            delay(1500)
            snap = CoreBridge.getSnapshot()
        }
    }

    val activeLocation: LocationItem? = remember(snap) {
        val locs = snap.locations
        when (snap.selectedLocation) {
            "recommended" -> locs.firstOrNull { it.code.equals("DE", ignoreCase = true) } ?: locs.firstOrNull()
            "fastest" -> locs.minByOrNull { if (it.latencyMs > 0) it.latencyMs else 999 }
            else -> locs.firstOrNull { it.code.equals(snap.selectedLocation, ignoreCase = true) } ?: locs.firstOrNull()
        }
    }

    val toggleTunnel: () -> Unit = {
        if (snap.state == "connected") {
            scope.launch {
                MinimalVpnService.turnOff(context)
                delay(250)
                snap = CoreBridge.getSnapshot()
            }
        } else {
            if (snap.accounts.isEmpty()) {
                tab = 1
                showAddForm = true
                authIsError = true
                authMessage = "Добавьте аккаунт Mozilla для подключения"
            } else {
                val prepareIntent = VpnService.prepare(context)
                if (prepareIntent != null) {
                    vpnPermissionLauncher.launch(prepareIntent)
                } else {
                    scope.launch {
                        isConnecting = true
                        MinimalVpnService.turnOn(context)
                        delay(450)
                        snap = CoreBridge.getSnapshot()
                        isConnecting = false
                    }
                }
            }
        }
    }

    Column(
        modifier = Modifier
            .fillMaxSize()
            .background(Color(0xFF090D16))
            .statusBarsPadding()
            .navigationBarsPadding()
    ) {
        // Top App Bar (matches MinimalAndroidPhone.tsx)
        Row(
            modifier = Modifier
                .fillMaxWidth()
                .height(54.dp)
                .background(Color(0xFF060911))
                .padding(horizontal = 20.dp),
            verticalAlignment = Alignment.CenterVertically,
            horizontalArrangement = Arrangement.SpaceBetween
        ) {
            Row(
                verticalAlignment = Alignment.CenterVertically,
                horizontalArrangement = Arrangement.spacedBy(10.dp)
            ) {
                ShieldIcon(
                    color = Color(0xFF818CF8),
                    size = 18.dp
                )
                Text(
                    text = "Mozilla VPN Lite",
                    color = Color.White,
                    fontSize = 16.sp,
                    fontWeight = FontWeight.Bold
                )
            }

            if (snap.state == "connected" && activeLocation != null) {
                Text(
                    text = "${activeLocation.flag} ${activeLocation.code} · ${activeLocation.latencyMs} мс",
                    color = Color(0xFF34D399),
                    fontSize = 13.sp,
                    fontFamily = FontFamily.Monospace,
                    fontWeight = FontWeight.SemiBold
                )
            } else {
                Text(
                    text = "Отключено",
                    color = Color(0xFF94A3B8),
                    fontSize = 13.sp,
                    fontFamily = FontFamily.Monospace
                )
            }
        }

        HorizontalDivider(color = Color(0xFF1E293B), thickness = 1.dp)

        // Main Screen Content
        Box(
            modifier = Modifier
                .weight(1f)
                .fillMaxWidth()
        ) {
            when (tab) {
                0 -> HomeTabScreen(
                    snap = snap,
                    activeLocation = activeLocation,
                    isConnecting = isConnecting,
                    onToggleTunnel = toggleTunnel,
                    onProtocolChange = { proto ->
                        scope.launch {
                            CoreBridge.setProtocol(proto)
                            snap = CoreBridge.getSnapshot()
                        }
                    },
                    onSelectAccount = { idx ->
                        scope.launch {
                            CoreBridge.selectAccount(idx)
                            snap = CoreBridge.getSnapshot()
                        }
                    },
                    onSelectLocation = { locCode ->
                        scope.launch {
                            CoreBridge.selectLocation(locCode)
                            snap = CoreBridge.getSnapshot()
                        }
                    }
                )

                1 -> AccountsTabScreen(
                    snap = snap,
                    showAddForm = showAddForm,
                    onToggleAddForm = {
                        showAddForm = !showAddForm
                        authMessage = null
                        authStepState = "idle"
                    },
                    authMethod = authMethod,
                    onAuthMethodChange = {
                        authMethod = it
                        authMessage = null
                        authStepState = "idle"
                    },
                    emailInput = emailInput,
                    onEmailChange = { emailInput = it },
                    passwordInput = passwordInput,
                    onPasswordChange = { passwordInput = it },
                    showPassword = showPassword,
                    onToggleShowPassword = { showPassword = !showPassword },
                    unblockCodeInput = unblockCodeInput,
                    onUnblockCodeChange = { unblockCodeInput = it },
                    verifyCodeInput = verifyCodeInput,
                    onVerifyCodeChange = { verifyCodeInput = it },
                    sessionTokenInput = sessionTokenInput,
                    onSessionTokenChange = { sessionTokenInput = it },
                    authStepState = authStepState,
                    authMessage = authMessage,
                    authIsError = authIsError,
                    isSigningIn = isSigningIn,
                    onSubmitPasswordLogin = {
                        scope.launch {
                            isSigningIn = true
                            authMessage = null
                            val res = CoreBridge.signInPassword(
                                emailInput.trim(),
                                passwordInput,
                                unblockCodeInput.trim()
                            )
                            isSigningIn = false
                            snap = CoreBridge.getSnapshot()
                            when (res.state) {
                                "ok" -> {
                                    authIsError = false
                                    authMessage = res.message
                                    authStepState = "idle"
                                    passwordInput = ""
                                    unblockCodeInput = ""
                                    verifyCodeInput = ""
                                    showAddForm = false
                                }
                                "verify_email", "verify_totp", "unblock_code" -> {
                                    authStepState = res.state
                                    authIsError = false
                                    authMessage = res.message
                                }
                                "need_browser" -> {
                                    authIsError = true
                                    authMessage = res.message
                                    authMethod = 1
                                }
                                else -> {
                                    authIsError = true
                                    authMessage = res.message
                                }
                            }
                        }
                    },
                    onSubmitVerifyCode = {
                        scope.launch {
                            isSigningIn = true
                            val res = CoreBridge.verifySignInCode(verifyCodeInput.trim())
                            isSigningIn = false
                            snap = CoreBridge.getSnapshot()
                            if (res.ok) {
                                authIsError = false
                                authMessage = res.message
                                authStepState = "idle"
                                verifyCodeInput = ""
                                passwordInput = ""
                                showAddForm = false
                            } else {
                                authIsError = true
                                authMessage = res.message
                            }
                        }
                    },
                    onOpenBrowserSignIn = {
                        showWebViewDialog = true
                    },
                    onSubmitSessionToken = {
                        scope.launch {
                            isSigningIn = true
                            authMessage = null
                            val res = CoreBridge.addAccountSessionToken(
                                emailInput.trim(),
                                sessionTokenInput.trim()
                            )
                            isSigningIn = false
                            snap = CoreBridge.getSnapshot()
                            when (res.state) {
                                "ok" -> {
                                    authIsError = false
                                    authMessage = res.message
                                    sessionTokenInput = ""
                                    showAddForm = false
                                }
                                "verify_email", "verify_totp" -> {
                                    showAddForm = true
                                    authMethod = 0
                                    authStepState = res.state
                                    authIsError = false
                                    authMessage = res.message
                                }
                                else -> {
                                    authIsError = true
                                    authMessage = res.message
                                }
                            }
                        }
                    },
                    onCancelVerifyStep = {
                        authStepState = "idle"
                        verifyCodeInput = ""
                        authMessage = null
                    },
                    onSelectAccount = { idx ->
                        scope.launch {
                            CoreBridge.selectAccount(idx)
                            snap = CoreBridge.getSnapshot()
                        }
                    },
                    onMoveAccount = { idx, dir ->
                        scope.launch {
                            CoreBridge.moveAccount(idx, dir)
                            snap = CoreBridge.getSnapshot()
                        }
                    },
                    onToggleAccountEnabled = { idx ->
                        scope.launch {
                            CoreBridge.toggleAccountEnabled(idx)
                            snap = CoreBridge.getSnapshot()
                        }
                    },
                    onRemoveAccount = { idx ->
                        scope.launch {
                            CoreBridge.removeAccount(idx)
                            snap = CoreBridge.getSnapshot()
                        }
                    },
                    onThresholdChange = { pct ->
                        scope.launch {
                            CoreBridge.setThreshold(pct)
                            snap = CoreBridge.getSnapshot()
                        }
                    }
                )

                2 -> ServersTabScreen(
                    snap = snap,
                    searchQuery = searchQuery,
                    onSearchChange = { searchQuery = it },
                    isPinging = isPinging,
                    onPingAll = {
                        scope.launch {
                            isPinging = true
                            CoreBridge.pingAll()
                            snap = CoreBridge.getSnapshot()
                            isPinging = false
                        }
                    },
                    onSelectLocation = { code ->
                        scope.launch {
                            CoreBridge.selectLocation(code)
                            snap = CoreBridge.getSnapshot()
                            tab = 0
                        }
                    }
                )
            }
        }

        HorizontalDivider(color = Color(0xFF1E293B), thickness = 1.dp)

        // Bottom Navigation Bar (matches MinimalAndroidPhone.tsx)
        Row(
            modifier = Modifier
                .fillMaxWidth()
                .height(64.dp)
                .background(Color(0xFF060911)),
            horizontalArrangement = Arrangement.SpaceAround,
            verticalAlignment = Alignment.CenterVertically
        ) {
            BottomNavTabItem(
                selected = tab == 0,
                label = "Пульт",
                icon = { color -> PowerIcon(color = color, size = 20.dp) },
                onClick = { tab = 0 },
                modifier = Modifier.weight(1f)
            )
            BottomNavTabItem(
                selected = tab == 1,
                label = "Аккаунты  (${snap.accounts.size})",
                icon = { color -> UsersIcon(color = color, size = 20.dp) },
                onClick = { tab = 1 },
                modifier = Modifier.weight(1f)
            )
            BottomNavTabItem(
                selected = tab == 2,
                label = "Серверы  (${snap.locations.size})",
                icon = { color -> GlobeIcon(color = color, size = 20.dp) },
                onClick = { tab = 2 },
                modifier = Modifier.weight(1f)
            )
        }
    }

    if (showWebViewDialog) {
        FxaWebViewDialog(
            initialEmail = emailInput.trim(),
            onDismiss = { showWebViewDialog = false },
            onSessionCaptured = { capturedEmail, capturedSessionToken ->
                showWebViewDialog = false
                if (capturedEmail.isNotBlank()) {
                    emailInput = capturedEmail
                }
                scope.launch {
                    isSigningIn = true
                    authMessage = "Проверка сессии Mozilla…"
                    authIsError = false
                    val res = CoreBridge.addAccountSessionToken(
                        capturedEmail.ifBlank { emailInput.trim() },
                        capturedSessionToken
                    )
                    isSigningIn = false
                    snap = CoreBridge.getSnapshot()
                    when (res.state) {
                        "ok" -> {
                            authIsError = false
                            authMessage = res.message
                            authStepState = "idle"
                            showAddForm = false
                        }
                        "verify_email", "verify_totp" -> {
                            showAddForm = true
                            authMethod = 0
                            authStepState = res.state
                            authIsError = false
                            authMessage = res.message
                        }
                        else -> {
                            authIsError = true
                            authMessage = res.message
                        }
                    }
                }
            }
        )
    }
}

@Composable
private fun BottomNavTabItem(
    selected: Boolean,
    label: String,
    icon: @Composable (Color) -> Unit,
    onClick: () -> Unit,
    modifier: Modifier = Modifier
) {
    val contentColor = if (selected) Color(0xFF818CF8) else Color(0xFF94A3B8)

    Column(
        modifier = modifier
            .fillMaxHeight()
            .clickable(onClick = onClick),
        horizontalAlignment = Alignment.CenterHorizontally,
        verticalArrangement = Arrangement.Center
    ) {
        icon(contentColor)
        Spacer(modifier = Modifier.height(4.dp))
        Text(
            text = label,
            color = contentColor,
            fontSize = 12.sp,
            fontWeight = if (selected) FontWeight.SemiBold else FontWeight.Normal,
            maxLines = 1,
            overflow = TextOverflow.Ellipsis
        )
    }
}

// ============================================================================
// TAB 1: ПУЛЬТ (HOME SCREEN — MATCHES MinimalAndroidPhone.tsx 1:1)
// ============================================================================
@Composable
private fun HomeTabScreen(
    snap: CoreSnapshot,
    activeLocation: LocationItem?,
    isConnecting: Boolean,
    onToggleTunnel: () -> Unit,
    onProtocolChange: (String) -> Unit,
    onSelectAccount: (Int) -> Unit,
    onSelectLocation: (String) -> Unit
) {
    val isConnected = snap.state == "connected"
    val heroBorderColor by animateColorAsState(
        targetValue = if (isConnected) Color(0xFF6366F1).copy(alpha = 0.6f) else Color(0xFF1E293B),
        label = "heroBorder"
    )

    LazyColumn(
        modifier = Modifier
            .fillMaxSize()
            .padding(horizontal = 20.dp),
        contentPadding = PaddingValues(vertical = 20.dp),
        verticalArrangement = Arrangement.spacedBy(20.dp)
    ) {
        // 1. Hero Connection Control (matches MinimalAndroidPhone.tsx lines 128-179)
        item {
            Column(
                modifier = Modifier
                    .fillMaxWidth()
                    .clip(RoundedCornerShape(20.dp))
                    .background(Color(0xFF060911))
                    .border(BorderStroke(1.dp, heroBorderColor), RoundedCornerShape(20.dp))
                    .padding(20.dp),
                horizontalAlignment = Alignment.CenterHorizontally,
                verticalArrangement = Arrangement.spacedBy(16.dp)
            ) {
                // 96dp Circular Power Button
                Box(
                    modifier = Modifier
                        .size(96.dp)
                        .clip(CircleShape)
                        .background(
                            when {
                                isConnecting -> Color(0xFF4F46E5).copy(alpha = 0.35f)
                                isConnected -> Color(0xFF4F46E5)
                                else -> Color(0xFF0F172A)
                            }
                        )
                        .border(
                            BorderStroke(
                                2.dp,
                                when {
                                    isConnecting -> Color(0xFF818CF8)
                                    isConnected -> Color(0xFF818CF8)
                                    else -> Color(0xFF334155)
                                }
                            ),
                            CircleShape
                        )
                        .clickable(onClick = onToggleTunnel),
                    contentAlignment = Alignment.Center
                ) {
                    if (isConnecting) {
                        CircularProgressIndicator(
                            modifier = Modifier.size(36.dp),
                            color = Color.White,
                            strokeWidth = 3.dp
                        )
                    } else {
                        PowerIcon(
                            color = if (isConnected) Color.White else Color(0xFF94A3B8),
                            size = 40.dp
                        )
                    }
                }

                Column(
                    horizontalAlignment = Alignment.CenterHorizontally,
                    verticalArrangement = Arrangement.spacedBy(4.dp)
                ) {
                    Text(
                        text = when {
                            isConnecting -> "Подключение…"
                            isConnected -> "Подключено · ${activeLocation?.name ?: "Германия"}"
                            else -> "Туннель отключён"
                        },
                        color = Color.White,
                        fontSize = 18.sp,
                        fontWeight = FontWeight.Bold,
                        textAlign = TextAlign.Center
                    )

                    if (activeLocation != null) {
                        Text(
                            text = "${activeLocation.host}:${activeLocation.port} · ${activeLocation.latencyMs} мс",
                            color = Color(0xFF94A3B8),
                            fontSize = 13.sp,
                            fontFamily = FontFamily.Monospace,
                            textAlign = TextAlign.Center
                        )
                    }
                }

                // Protocol Segmented Switch (HTTP/3 MASQUE vs HTTP/2 CONNECT)
                Row(
                    modifier = Modifier
                        .fillMaxWidth()
                        .clip(RoundedCornerShape(12.dp))
                        .background(Color(0xFF0F172A))
                        .border(BorderStroke(1.dp, Color(0xFF1E293B)), RoundedCornerShape(12.dp))
                        .padding(4.dp),
                    horizontalArrangement = Arrangement.spacedBy(4.dp)
                ) {
                    val isH3 = snap.protocol == "h3"
                    Box(
                        modifier = Modifier
                            .weight(1f)
                            .clip(RoundedCornerShape(8.dp))
                            .background(if (isH3) Color(0xFF1E293B) else Color.Transparent)
                            .clickable { onProtocolChange("h3") }
                            .padding(vertical = 10.dp),
                        contentAlignment = Alignment.Center
                    ) {
                        Text(
                            text = "HTTP/3 MASQUE",
                            color = if (isH3) Color.White else Color(0xFF94A3B8),
                            fontSize = 13.sp,
                            fontWeight = if (isH3) FontWeight.SemiBold else FontWeight.Medium
                        )
                    }
                    Box(
                        modifier = Modifier
                            .weight(1f)
                            .clip(RoundedCornerShape(8.dp))
                            .background(if (!isH3) Color(0xFF1E293B) else Color.Transparent)
                            .clickable { onProtocolChange("h2") }
                            .padding(vertical = 10.dp),
                        contentAlignment = Alignment.Center
                    ) {
                        Text(
                            text = "HTTP/2 CONNECT",
                            color = if (!isH3) Color.White else Color(0xFF94A3B8),
                            fontSize = 13.sp,
                            fontWeight = if (!isH3) FontWeight.SemiBold else FontWeight.Medium
                        )
                    }
                }
            }
        }

        // 2. Instant 1-Tap Mozilla Profile Switcher (no header label, matches MinimalAndroidPhone.tsx lines 182-242)
        if (snap.accounts.isNotEmpty()) {
            item {
                Column(verticalArrangement = Arrangement.spacedBy(8.dp)) {
                    snap.accounts.forEachIndexed { idx, acc ->
                        val isActive = idx == snap.activeAccount
                        val pct = ((acc.usedBytes.toDouble() / acc.limitBytes.coerceAtLeast(1L).toDouble()) * 100.0)
                            .roundToInt()
                            .coerceIn(0, 100)
                        val remainingBytes = (acc.limitBytes - acc.usedBytes).coerceAtLeast(0L)
                        val barColor = when {
                            pct >= snap.thresholdPct -> Color(0xFFF43F5E)
                            pct >= 75 -> Color(0xFFFBBF24)
                            else -> Color(0xFF6366F1)
                        }

                        Column(
                            modifier = Modifier
                                .fillMaxWidth()
                                .clip(RoundedCornerShape(16.dp))
                                .background(
                                    if (isActive) Color(0xFF4F46E5).copy(alpha = 0.15f)
                                    else Color(0xFF060911).copy(alpha = 0.6f)
                                )
                                .border(
                                    BorderStroke(
                                        1.dp,
                                        if (isActive) Color(0xFF6366F1).copy(alpha = 0.8f)
                                        else Color(0xFF1E293B)
                                    ),
                                    RoundedCornerShape(16.dp)
                                )
                                .clickable { onSelectAccount(idx) }
                                .padding(14.dp),
                            verticalArrangement = Arrangement.spacedBy(8.dp)
                        ) {
                            Row(
                                modifier = Modifier.fillMaxWidth(),
                                horizontalArrangement = Arrangement.SpaceBetween,
                                verticalAlignment = Alignment.CenterVertically
                            ) {
                                Row(
                                    verticalAlignment = Alignment.CenterVertically,
                                    horizontalArrangement = Arrangement.spacedBy(8.dp),
                                    modifier = Modifier.weight(1f)
                                ) {
                                    Text(
                                        text = "#${acc.position}",
                                        color = Color(0xFF94A3B8),
                                        fontSize = 13.sp,
                                        fontFamily = FontFamily.Monospace
                                    )
                                    Text(
                                        text = acc.email,
                                        color = Color.White,
                                        fontSize = 15.sp,
                                        fontWeight = FontWeight.SemiBold,
                                        maxLines = 1,
                                        overflow = TextOverflow.Ellipsis
                                    )
                                }

                                Text(
                                    text = when {
                                        isActive -> "Активен"
                                        acc.exhausted -> "Квота исчерпана"
                                        else -> "Готов"
                                    },
                                    color = Color(0xFF94A3B8),
                                    fontSize = 13.sp,
                                    fontFamily = FontFamily.Monospace
                                )
                            }

                            // Quota Progress Bar
                            Box(
                                modifier = Modifier
                                    .fillMaxWidth()
                                    .height(6.dp)
                                    .clip(CircleShape)
                                    .background(Color(0xFF1E293B))
                            ) {
                                Box(
                                    modifier = Modifier
                                        .fillMaxWidth(pct / 100f)
                                        .fillMaxHeight()
                                        .clip(CircleShape)
                                        .background(barColor)
                                )
                            }

                            Row(
                                modifier = Modifier.fillMaxWidth(),
                                horizontalArrangement = Arrangement.SpaceBetween
                            ) {
                                Text(
                                    text = "Осталось: ${CoreBridge.formatBytes(remainingBytes)} из ${CoreBridge.formatBytes(acc.limitBytes)}",
                                    color = Color(0xFF94A3B8),
                                    fontSize = 12.sp,
                                    fontFamily = FontFamily.Monospace
                                )
                                Text(
                                    text = "$pct% · порог ${snap.thresholdPct}%",
                                    color = Color(0xFF94A3B8),
                                    fontSize = 12.sp,
                                    fontFamily = FontFamily.Monospace
                                )
                            }
                        }
                    }
                }
            }
        }

        // 3. Instant 1-Tap Server / Location Switcher (no header label, stacked layout matching MinimalAndroidPhone.tsx lines 245-296)
        item {
            Column(verticalArrangement = Arrangement.spacedBy(8.dp)) {
                val topFive = snap.locations.take(5)

                // Row 1: Fastest + Location 0
                Row(
                    modifier = Modifier.fillMaxWidth(),
                    horizontalArrangement = Arrangement.spacedBy(8.dp)
                ) {
                    val isFastestSelected = snap.selectedLocation == "fastest"
                    QuickLocationTile(
                        flag = "⚡",
                        title = "Быстрейший",
                        subtitle = "Авто пинг",
                        selected = isFastestSelected,
                        onClick = { onSelectLocation("fastest") },
                        modifier = Modifier.weight(1f)
                    )
                    if (topFive.isNotEmpty()) {
                        val loc0 = topFive[0]
                        val selected0 = snap.selectedLocation.equals(loc0.code, ignoreCase = true) ||
                            (snap.selectedLocation == "recommended" && loc0.code.equals("DE", ignoreCase = true))
                        QuickLocationTile(
                            flag = loc0.flag,
                            title = loc0.name,
                            subtitle = "${loc0.latencyMs} мс",
                            selected = selected0,
                            onClick = { onSelectLocation(loc0.code) },
                            modifier = Modifier.weight(1f)
                        )
                    }
                }

                // Row 2: Location 1 + Location 2
                if (topFive.size >= 3) {
                    Row(
                        modifier = Modifier.fillMaxWidth(),
                        horizontalArrangement = Arrangement.spacedBy(8.dp)
                    ) {
                        listOf(topFive[1], topFive[2]).forEach { loc ->
                            val isSelected = snap.selectedLocation.equals(loc.code, ignoreCase = true)
                            QuickLocationTile(
                                flag = loc.flag,
                                title = loc.name,
                                subtitle = "${loc.latencyMs} мс",
                                selected = isSelected,
                                onClick = { onSelectLocation(loc.code) },
                                modifier = Modifier.weight(1f)
                            )
                        }
                    }
                }

                // Row 3: Location 3 + Location 4
                if (topFive.size >= 5) {
                    Row(
                        modifier = Modifier.fillMaxWidth(),
                        horizontalArrangement = Arrangement.spacedBy(8.dp)
                    ) {
                        listOf(topFive[3], topFive[4]).forEach { loc ->
                            val isSelected = snap.selectedLocation.equals(loc.code, ignoreCase = true)
                            QuickLocationTile(
                                flag = loc.flag,
                                title = loc.name,
                                subtitle = "${loc.latencyMs} мс",
                                selected = isSelected,
                                onClick = { onSelectLocation(loc.code) },
                                modifier = Modifier.weight(1f)
                            )
                        }
                    }
                }
            }
        }
    }
}

@Composable
private fun QuickLocationTile(
    flag: String,
    title: String,
    subtitle: String,
    selected: Boolean,
    onClick: () -> Unit,
    modifier: Modifier = Modifier
) {
    Row(
        modifier = modifier
            .heightIn(min = 58.dp)
            .clip(RoundedCornerShape(14.dp))
            .background(
                if (selected) Color(0xFF4F46E5).copy(alpha = 0.15f)
                else Color(0xFF060911).copy(alpha = 0.6f)
            )
            .border(
                BorderStroke(
                    1.dp,
                    if (selected) Color(0xFF6366F1)
                    else Color(0xFF1E293B)
                ),
                RoundedCornerShape(14.dp)
            )
            .clickable(onClick = onClick)
            .padding(horizontal = 12.dp, vertical = 10.dp),
        verticalAlignment = Alignment.CenterVertically,
        horizontalArrangement = Arrangement.spacedBy(10.dp)
    ) {
        Text(text = flag, fontSize = 18.sp)
        Column(
            modifier = Modifier.weight(1f),
            verticalArrangement = Arrangement.spacedBy(2.dp)
        ) {
            Text(
                text = title,
                color = Color.White,
                fontSize = 14.sp,
                fontWeight = FontWeight.SemiBold,
                maxLines = 1,
                overflow = TextOverflow.Ellipsis
            )
            Text(
                text = subtitle,
                color = Color(0xFF94A3B8),
                fontSize = 12.sp,
                fontFamily = FontFamily.Monospace,
                maxLines = 1
            )
        }
    }
}

// ============================================================================
// TAB 2: АККАУНТЫ (ACCOUNTS & ROTATION SCREEN)
// ============================================================================
@Composable
private fun AccountsTabScreen(
    snap: CoreSnapshot,
    showAddForm: Boolean,
    onToggleAddForm: () -> Unit,
    authMethod: Int,
    onAuthMethodChange: (Int) -> Unit,
    emailInput: String,
    onEmailChange: (String) -> Unit,
    passwordInput: String,
    onPasswordChange: (String) -> Unit,
    showPassword: Boolean,
    onToggleShowPassword: () -> Unit,
    unblockCodeInput: String,
    onUnblockCodeChange: (String) -> Unit,
    verifyCodeInput: String,
    onVerifyCodeChange: (String) -> Unit,
    sessionTokenInput: String,
    onSessionTokenChange: (String) -> Unit,
    authStepState: String,
    authMessage: String?,
    authIsError: Boolean,
    isSigningIn: Boolean,
    onSubmitPasswordLogin: () -> Unit,
    onSubmitVerifyCode: () -> Unit,
    onOpenBrowserSignIn: () -> Unit,
    onSubmitSessionToken: () -> Unit,
    onCancelVerifyStep: () -> Unit,
    onSelectAccount: (Int) -> Unit,
    onMoveAccount: (Int, Int) -> Unit,
    onToggleAccountEnabled: (Int) -> Unit,
    onRemoveAccount: (Int) -> Unit,
    onThresholdChange: (Int) -> Unit
) {
    LazyColumn(
        modifier = Modifier
            .fillMaxSize()
            .padding(horizontal = 20.dp),
        contentPadding = PaddingValues(vertical = 20.dp),
        verticalArrangement = Arrangement.spacedBy(16.dp)
    ) {
        // Header Row (matches MinimalAndroidPhone.tsx lines 305-322)
        item {
            Row(
                modifier = Modifier.fillMaxWidth(),
                horizontalArrangement = Arrangement.SpaceBetween,
                verticalAlignment = Alignment.CenterVertically
            ) {
                Column(
                    modifier = Modifier.weight(1f),
                    verticalArrangement = Arrangement.spacedBy(3.dp)
                ) {
                    Text(
                        text = "Профили Mozilla (${snap.accounts.size})",
                        color = Color.White,
                        fontSize = 17.sp,
                        fontWeight = FontWeight.Bold
                    )
                    Text(
                        text = "Каждый аккаунт даёт 5 ГБ/мес · Авто-смена по очереди",
                        color = Color(0xFF94A3B8),
                        fontSize = 13.sp
                    )
                }

                Spacer(modifier = Modifier.width(10.dp))

                Box(
                    modifier = Modifier
                        .clip(RoundedCornerShape(12.dp))
                        .background(Color(0xFF4F46E5))
                        .clickable(onClick = onToggleAddForm)
                        .padding(horizontal = 14.dp, vertical = 10.dp)
                ) {
                    Text(
                        text = if (showAddForm) "Закрыть" else "+ Добавить",
                        color = Color.White,
                        fontSize = 13.sp,
                        fontWeight = FontWeight.SemiBold
                    )
                }
            }
        }

        // Status / Error Feedback Banner
        if (!authMessage.isNullOrBlank()) {
            item {
                Box(
                    modifier = Modifier
                        .fillMaxWidth()
                        .clip(RoundedCornerShape(14.dp))
                        .background(
                            if (authIsError) Color(0xFFEF4444).copy(alpha = 0.15f)
                            else Color(0xFF10B981).copy(alpha = 0.15f)
                        )
                        .border(
                            BorderStroke(
                                1.dp,
                                if (authIsError) Color(0xFFEF4444).copy(alpha = 0.4f)
                                else Color(0xFF10B981).copy(alpha = 0.4f)
                            ),
                            RoundedCornerShape(14.dp)
                        )
                        .padding(14.dp)
                ) {
                    Text(
                        text = authMessage,
                        color = if (authIsError) Color(0xFFFCA5A5) else Color(0xFF6EE7B7),
                        fontSize = 13.sp,
                        lineHeight = 18.sp
                    )
                }
            }
        }

        // Add Account Form
        if (showAddForm || snap.accounts.isEmpty() || authStepState != "idle") {
            item {
                Column(
                    modifier = Modifier
                        .fillMaxWidth()
                        .clip(RoundedCornerShape(18.dp))
                        .background(Color(0xFF060911))
                        .border(BorderStroke(1.dp, Color(0xFF1E293B)), RoundedCornerShape(18.dp))
                        .padding(16.dp),
                    verticalArrangement = Arrangement.spacedBy(12.dp)
                ) {
                    Text(
                        text = "Добавление аккаунта Firefox (Mozilla VPN)",
                        color = Color.White,
                        fontSize = 14.sp,
                        fontWeight = FontWeight.SemiBold
                    )

                    if (authStepState == "verify_email" || authStepState == "verify_totp") {
                        Text(
                            text = if (authStepState == "verify_totp")
                                "Введите 6-значный код двухфакторной аутентификации (2FA TOTP):"
                            else
                                "Введите 6-значный код подтверждения входа из письма от Mozilla:",
                            color = Color(0xFFE2E8F0),
                            fontSize = 13.sp,
                            lineHeight = 18.sp
                        )

                        OutlinedTextField(
                            value = verifyCodeInput,
                            onValueChange = onVerifyCodeChange,
                            label = { Text("6-значный код из письма Mozilla", fontSize = 13.sp) },
                            singleLine = true,
                            keyboardOptions = KeyboardOptions(
                                keyboardType = KeyboardType.Number,
                                imeAction = ImeAction.Done
                            ),
                            modifier = Modifier.fillMaxWidth(),
                            shape = RoundedCornerShape(12.dp)
                        )

                        Row(
                            modifier = Modifier.fillMaxWidth(),
                            horizontalArrangement = Arrangement.spacedBy(10.dp)
                        ) {
                            OutlinedButton(
                                onClick = onCancelVerifyStep,
                                modifier = Modifier.weight(1f),
                                shape = RoundedCornerShape(12.dp)
                            ) {
                                Text("Назад", fontSize = 13.sp, color = Color(0xFFCBD5E1))
                            }

                            Button(
                                onClick = onSubmitVerifyCode,
                                enabled = !isSigningIn,
                                modifier = Modifier.weight(2f),
                                shape = RoundedCornerShape(12.dp),
                                colors = ButtonDefaults.buttonColors(containerColor = Color(0xFF4F46E5))
                            ) {
                                if (isSigningIn) {
                                    CircularProgressIndicator(
                                        modifier = Modifier.size(16.dp),
                                        color = Color.White,
                                        strokeWidth = 2.dp
                                    )
                                    Spacer(modifier = Modifier.width(8.dp))
                                }
                                Text(
                                    "Подтвердить код",
                                    fontSize = 13.sp,
                                    color = Color.White,
                                    fontWeight = FontWeight.SemiBold
                                )
                            }
                        }
                    } else {
                        // 3-Mode Auth Selector
                        Row(
                            modifier = Modifier
                                .fillMaxWidth()
                                .clip(RoundedCornerShape(12.dp))
                                .background(Color(0xFF0F172A))
                                .border(BorderStroke(1.dp, Color(0xFF1E293B)), RoundedCornerShape(12.dp))
                                .padding(4.dp),
                            horizontalArrangement = Arrangement.spacedBy(4.dp)
                        ) {
                            val methods = listOf(
                                0 to "Прямой вход",
                                1 to "Браузер",
                                2 to "Токен"
                            )
                            methods.forEach { (id, title) ->
                                val selected = authMethod == id
                                Box(
                                    modifier = Modifier
                                        .weight(1f)
                                        .clip(RoundedCornerShape(8.dp))
                                        .background(if (selected) Color(0xFF1E293B) else Color.Transparent)
                                        .clickable { onAuthMethodChange(id) }
                                        .padding(vertical = 8.dp),
                                    contentAlignment = Alignment.Center
                                ) {
                                    Text(
                                        text = title,
                                        color = if (selected) Color.White else Color(0xFF94A3B8),
                                        fontSize = 13.sp,
                                        fontWeight = if (selected) FontWeight.SemiBold else FontWeight.Medium,
                                        maxLines = 1
                                    )
                                }
                            }
                        }

                        when (authMethod) {
                            0 -> {
                                OutlinedTextField(
                                    value = emailInput,
                                    onValueChange = onEmailChange,
                                    label = { Text("Email аккаунта Mozilla", fontSize = 13.sp) },
                                    singleLine = true,
                                    keyboardOptions = KeyboardOptions(
                                        keyboardType = KeyboardType.Email,
                                        imeAction = ImeAction.Next
                                    ),
                                    modifier = Modifier.fillMaxWidth(),
                                    shape = RoundedCornerShape(12.dp)
                                )

                                OutlinedTextField(
                                    value = passwordInput,
                                    onValueChange = onPasswordChange,
                                    label = { Text("Пароль аккаунта", fontSize = 13.sp) },
                                    singleLine = true,
                                    visualTransformation = if (showPassword) {
                                        VisualTransformation.None
                                    } else {
                                        PasswordVisualTransformation()
                                    },
                                    keyboardOptions = KeyboardOptions(
                                        keyboardType = KeyboardType.Password,
                                        imeAction = ImeAction.Done
                                    ),
                                    trailingIcon = {
                                        Text(
                                            text = if (showPassword) "Скрыть" else "Показать",
                                            color = Color(0xFF818CF8),
                                            fontSize = 13.sp,
                                            fontWeight = FontWeight.Medium,
                                            modifier = Modifier
                                                .clickable(onClick = onToggleShowPassword)
                                                .padding(horizontal = 12.dp, vertical = 6.dp)
                                        )
                                    },
                                    modifier = Modifier.fillMaxWidth(),
                                    shape = RoundedCornerShape(12.dp)
                                )

                                if (authStepState == "unblock_code") {
                                    OutlinedTextField(
                                        value = unblockCodeInput,
                                        onValueChange = onUnblockCodeChange,
                                        label = { Text("Код разблокировки из письма (8 символов)", fontSize = 13.sp) },
                                        singleLine = true,
                                        modifier = Modifier.fillMaxWidth(),
                                        shape = RoundedCornerShape(12.dp)
                                    )
                                }

                                Button(
                                    onClick = onSubmitPasswordLogin,
                                    enabled = !isSigningIn && emailInput.isNotBlank() && passwordInput.isNotEmpty(),
                                    modifier = Modifier
                                        .fillMaxWidth()
                                        .height(48.dp),
                                    shape = RoundedCornerShape(12.dp),
                                    colors = ButtonDefaults.buttonColors(
                                        containerColor = Color(0xFF4F46E5),
                                        disabledContainerColor = Color(0xFF4F46E5).copy(alpha = 0.4f)
                                    )
                                ) {
                                    if (isSigningIn) {
                                        CircularProgressIndicator(
                                            modifier = Modifier.size(18.dp),
                                            color = Color.White,
                                            strokeWidth = 2.dp
                                        )
                                        Spacer(modifier = Modifier.width(10.dp))
                                        Text(
                                            "Вход в аккаунт Mozilla…",
                                            fontSize = 14.sp,
                                            color = Color.White,
                                            fontWeight = FontWeight.SemiBold
                                        )
                                    } else {
                                        Text(
                                            "Сохранить и получить ProxyPass",
                                            fontSize = 14.sp,
                                            color = Color.White,
                                            fontWeight = FontWeight.SemiBold
                                        )
                                    }
                                }
                            }

                            1 -> {
                                Text(
                                    text = "Откроется окно входа accounts.firefox.com. После ввода пароля приложение перехватит токен и предложит ввести 6-значный код из письма Mozilla.",
                                    color = Color(0xFF94A3B8),
                                    fontSize = 13.sp,
                                    lineHeight = 18.sp
                                )

                                Button(
                                    onClick = onOpenBrowserSignIn,
                                    enabled = !isSigningIn,
                                    modifier = Modifier
                                        .fillMaxWidth()
                                        .height(48.dp),
                                    shape = RoundedCornerShape(12.dp),
                                    colors = ButtonDefaults.buttonColors(containerColor = Color(0xFF4F46E5))
                                ) {
                                    Text(
                                        text = "Открыть вход Mozilla (WebView)",
                                        fontSize = 14.sp,
                                        color = Color.White,
                                        fontWeight = FontWeight.SemiBold
                                    )
                                }
                            }

                            2 -> {
                                OutlinedTextField(
                                    value = emailInput,
                                    onValueChange = onEmailChange,
                                    label = { Text("Email аккаунта (необязательно)", fontSize = 13.sp) },
                                    singleLine = true,
                                    modifier = Modifier.fillMaxWidth(),
                                    shape = RoundedCornerShape(12.dp)
                                )

                                OutlinedTextField(
                                    value = sessionTokenInput,
                                    onValueChange = onSessionTokenChange,
                                    label = { Text("64-hex sessionToken из WebChannel", fontSize = 13.sp) },
                                    singleLine = true,
                                    modifier = Modifier.fillMaxWidth(),
                                    shape = RoundedCornerShape(12.dp)
                                )

                                Button(
                                    onClick = onSubmitSessionToken,
                                    enabled = !isSigningIn && sessionTokenInput.trim().length >= 32,
                                    modifier = Modifier
                                        .fillMaxWidth()
                                        .height(48.dp),
                                    shape = RoundedCornerShape(12.dp),
                                    colors = ButtonDefaults.buttonColors(containerColor = Color(0xFF4F46E5))
                                ) {
                                    Text(
                                        text = "Сохранить и получить ProxyPass",
                                        fontSize = 14.sp,
                                        color = Color.White,
                                        fontWeight = FontWeight.SemiBold
                                    )
                                }
                            }
                        }
                    }
                }
            }
        }

        // Account Cards List (matches MinimalAndroidPhone.tsx lines 408-519)
        itemsIndexed(snap.accounts) { idx, acc ->
            val isActive = idx == snap.activeAccount
            val pct = ((acc.usedBytes.toDouble() / acc.limitBytes.coerceAtLeast(1L).toDouble()) * 100.0)
                .roundToInt()
                .coerceIn(0, 100)

            Column(
                modifier = Modifier
                    .fillMaxWidth()
                    .clip(RoundedCornerShape(18.dp))
                    .background(
                        if (isActive) Color(0xFF4F46E5).copy(alpha = 0.15f)
                        else Color(0xFF060911).copy(alpha = 0.7f)
                    )
                    .border(
                        BorderStroke(
                            1.dp,
                            if (isActive) Color(0xFF6366F1).copy(alpha = 0.8f)
                            else Color(0xFF1E293B)
                        ),
                        RoundedCornerShape(18.dp)
                    )
                    .padding(16.dp),
                verticalArrangement = Arrangement.spacedBy(12.dp)
            ) {
                Row(
                    modifier = Modifier.fillMaxWidth(),
                    horizontalArrangement = Arrangement.SpaceBetween,
                    verticalAlignment = Alignment.Top
                ) {
                    Column(
                        modifier = Modifier
                            .weight(1f)
                            .clickable { onSelectAccount(idx) },
                        verticalArrangement = Arrangement.spacedBy(4.dp)
                    ) {
                        Row(
                            verticalAlignment = Alignment.CenterVertically,
                            horizontalArrangement = Arrangement.spacedBy(8.dp)
                        ) {
                            Text(
                                text = "#${acc.position}",
                                color = Color(0xFF94A3B8),
                                fontSize = 13.sp,
                                fontFamily = FontFamily.Monospace
                            )
                            Text(
                                text = acc.email,
                                color = Color.White,
                                fontSize = 15.sp,
                                fontWeight = FontWeight.Bold,
                                maxLines = 1,
                                overflow = TextOverflow.Ellipsis
                            )
                        }
                        Text(
                            text = if (isActive) "Активный профиль" else if (acc.exhausted) "В резерве до сброса" else "В очереди ротации",
                            color = Color(0xFF94A3B8),
                            fontSize = 12.sp,
                            fontFamily = FontFamily.Monospace
                        )
                    }

                    if (!isActive) {
                        Box(
                            modifier = Modifier
                                .clip(RoundedCornerShape(10.dp))
                                .background(Color(0xFF4F46E5))
                                .clickable { onSelectAccount(idx) }
                                .padding(horizontal = 12.dp, vertical = 6.dp)
                        ) {
                            Text(
                                text = "Включить",
                                color = Color.White,
                                fontSize = 13.sp,
                                fontWeight = FontWeight.SemiBold
                            )
                        }
                    } else {
                        Text(
                            text = "✓ Используется",
                            color = Color(0xFFA5B4FC),
                            fontSize = 13.sp,
                            fontWeight = FontWeight.SemiBold
                        )
                    }
                }

                // Quota Progress Bar
                Column(verticalArrangement = Arrangement.spacedBy(6.dp)) {
                    Box(
                        modifier = Modifier
                            .fillMaxWidth()
                            .height(6.dp)
                            .clip(CircleShape)
                            .background(Color(0xFF1E293B))
                    ) {
                        Box(
                            modifier = Modifier
                                .fillMaxWidth(pct / 100f)
                                .fillMaxHeight()
                                .clip(CircleShape)
                                .background(
                                    if (pct >= snap.thresholdPct) Color(0xFFF43F5E)
                                    else Color(0xFF6366F1)
                                )
                        )
                    }
                    Row(
                        modifier = Modifier.fillMaxWidth(),
                        horizontalArrangement = Arrangement.SpaceBetween
                    ) {
                        Text(
                            text = "${CoreBridge.formatBytes(acc.usedBytes)} / ${CoreBridge.formatBytes(acc.limitBytes)} ($pct%)",
                            color = Color(0xFF94A3B8),
                            fontSize = 12.sp,
                            fontFamily = FontFamily.Monospace
                        )
                    }
                }

                HorizontalDivider(color = Color(0xFF1E293B), thickness = 1.dp)

                // Priority Up/Down, Rotation Toggle, Delete
                Row(
                    modifier = Modifier.fillMaxWidth(),
                    horizontalArrangement = Arrangement.SpaceBetween,
                    verticalAlignment = Alignment.CenterVertically
                ) {
                    Row(
                        horizontalArrangement = Arrangement.spacedBy(6.dp),
                        verticalAlignment = Alignment.CenterVertically
                    ) {
                        Box(
                            modifier = Modifier
                                .clip(RoundedCornerShape(8.dp))
                                .background(Color(0xFF0F172A))
                                .clickable(enabled = idx > 0) { onMoveAccount(idx, -1) }
                                .padding(horizontal = 10.dp, vertical = 6.dp)
                        ) {
                            Text(
                                text = "↑",
                                color = if (idx > 0) Color(0xFFCBD5E1) else Color(0xFF475569),
                                fontSize = 14.sp
                            )
                        }
                        Box(
                            modifier = Modifier
                                .clip(RoundedCornerShape(8.dp))
                                .background(Color(0xFF0F172A))
                                .clickable(enabled = idx < snap.accounts.size - 1) { onMoveAccount(idx, 1) }
                                .padding(horizontal = 10.dp, vertical = 6.dp)
                        ) {
                            Text(
                                text = "↓",
                                color = if (idx < snap.accounts.size - 1) Color(0xFFCBD5E1) else Color(0xFF475569),
                                fontSize = 14.sp
                            )
                        }
                        Box(
                            modifier = Modifier
                                .clip(RoundedCornerShape(8.dp))
                                .background(Color(0xFF0F172A))
                                .clickable { onToggleAccountEnabled(idx) }
                                .padding(horizontal = 12.dp, vertical = 6.dp)
                        ) {
                            Text(
                                text = if (acc.enabled) "В ротации" else "Отключён",
                                color = Color(0xFFCBD5E1),
                                fontSize = 13.sp,
                                fontWeight = FontWeight.Medium
                            )
                        }
                    }

                    Box(
                        modifier = Modifier
                            .clip(RoundedCornerShape(8.dp))
                            .clickable { onRemoveAccount(idx) }
                            .padding(horizontal = 10.dp, vertical = 6.dp)
                    ) {
                        Text(
                            text = "Удалить",
                            color = Color(0xFF94A3B8),
                            fontSize = 13.sp
                        )
                    }
                }
            }
        }

        // Threshold Slider Card (matches MinimalAndroidPhone.tsx lines 522-544)
        item {
            Column(
                modifier = Modifier
                    .fillMaxWidth()
                    .clip(RoundedCornerShape(18.dp))
                    .background(Color(0xFF060911).copy(alpha = 0.7f))
                    .border(BorderStroke(1.dp, Color(0xFF1E293B)), RoundedCornerShape(18.dp))
                    .padding(16.dp),
                verticalArrangement = Arrangement.spacedBy(8.dp)
            ) {
                Row(
                    modifier = Modifier.fillMaxWidth(),
                    horizontalArrangement = Arrangement.SpaceBetween,
                    verticalAlignment = Alignment.CenterVertically
                ) {
                    Text(
                        text = "Порог авто-переключения аккаунта",
                        color = Color(0xFFE2E8F0),
                        fontSize = 13.sp,
                        fontWeight = FontWeight.SemiBold
                    )
                    Text(
                        text = "${snap.thresholdPct}% квоты",
                        color = Color(0xFF818CF8),
                        fontSize = 13.sp,
                        fontFamily = FontFamily.Monospace,
                        fontWeight = FontWeight.Bold
                    )
                }

                Slider(
                    value = snap.thresholdPct.toFloat(),
                    onValueChange = { onThresholdChange(it.roundToInt()) },
                    valueRange = 50f..100f,
                    steps = 9,
                    colors = SliderDefaults.colors(
                        thumbColor = Color(0xFF6366F1),
                        activeTrackColor = Color(0xFF6366F1),
                        inactiveTrackColor = Color(0xFF1E293B)
                    )
                )

                Text(
                    text = "При достижении ${snap.thresholdPct}% из 5 ГБ ядро автоматически запрашивает ProxyPass следующего аккаунта в списке.",
                    color = Color(0xFF94A3B8),
                    fontSize = 12.sp,
                    lineHeight = 16.sp
                )
            }
        }
    }
}

// ============================================================================
// TAB 3: СЕРВЕРЫ (32 LOCATIONS SCREEN — MATCHES MinimalAndroidPhone.tsx)
// ============================================================================
@Composable
private fun ServersTabScreen(
    snap: CoreSnapshot,
    searchQuery: String,
    onSearchChange: (String) -> Unit,
    isPinging: Boolean,
    onPingAll: () -> Unit,
    onSelectLocation: (String) -> Unit
) {
    val filteredLocations = remember(snap.locations, searchQuery) {
        val q = searchQuery.trim().lowercase()
        if (q.isEmpty()) {
            snap.locations
        } else {
            snap.locations.filter {
                it.name.lowercase().contains(q) ||
                    it.city.lowercase().contains(q) ||
                    it.code.lowercase().contains(q) ||
                    it.host.lowercase().contains(q)
            }
        }
    }

    Column(
        modifier = Modifier
            .fillMaxSize()
            .padding(horizontal = 20.dp, vertical = 20.dp),
        verticalArrangement = Arrangement.spacedBy(14.dp)
    ) {
        // Header + Ping Button (matches MinimalAndroidPhone.tsx lines 553-571)
        Row(
            modifier = Modifier.fillMaxWidth(),
            horizontalArrangement = Arrangement.SpaceBetween,
            verticalAlignment = Alignment.CenterVertically
        ) {
            Column(
                modifier = Modifier.weight(1f),
                verticalArrangement = Arrangement.spacedBy(3.dp)
            ) {
                Text(
                    text = "Локации Mozilla (${snap.locations.size})",
                    color = Color.White,
                    fontSize = 17.sp,
                    fontWeight = FontWeight.Bold
                )
                Text(
                    text = "Все страны из vpn-serverlist разблокированы",
                    color = Color(0xFF94A3B8),
                    fontSize = 13.sp
                )
            }

            Spacer(modifier = Modifier.width(10.dp))

            Row(
                modifier = Modifier
                    .clip(RoundedCornerShape(12.dp))
                    .background(Color(0xFF4F46E5))
                    .clickable(enabled = !isPinging, onClick = onPingAll)
                    .padding(horizontal = 14.dp, vertical = 10.dp),
                verticalAlignment = Alignment.CenterVertically,
                horizontalArrangement = Arrangement.spacedBy(6.dp)
            ) {
                if (isPinging) {
                    CircularProgressIndicator(
                        modifier = Modifier.size(14.dp),
                        color = Color.White,
                        strokeWidth = 2.dp
                    )
                } else {
                    Text(text = "↻", color = Color.White, fontSize = 14.sp)
                }
                Text(
                    text = "Пинг",
                    color = Color.White,
                    fontSize = 13.sp,
                    fontWeight = FontWeight.SemiBold
                )
            }
        }

        // Search Box
        OutlinedTextField(
            value = searchQuery,
            onValueChange = onSearchChange,
            placeholder = {
                Text(
                    "Поиск страны, города или Fastly-узла…",
                    fontSize = 13.sp,
                    color = Color(0xFF64748B)
                )
            },
            singleLine = true,
            modifier = Modifier.fillMaxWidth(),
            shape = RoundedCornerShape(14.dp)
        )

        // 2-Column Smart Modes (Recommended & Fastest)
        Row(
            modifier = Modifier.fillMaxWidth(),
            horizontalArrangement = Arrangement.spacedBy(8.dp)
        ) {
            val isRecommended = snap.selectedLocation == "recommended"
            val isFastest = snap.selectedLocation == "fastest"

            Column(
                modifier = Modifier
                    .weight(1f)
                    .clip(RoundedCornerShape(14.dp))
                    .background(
                        if (isRecommended) Color(0xFF4F46E5).copy(alpha = 0.2f)
                        else Color(0xFF060911).copy(alpha = 0.6f)
                    )
                    .border(
                        BorderStroke(
                            1.dp,
                            if (isRecommended) Color(0xFF6366F1)
                            else Color(0xFF1E293B)
                        ),
                        RoundedCornerShape(14.dp)
                    )
                    .clickable { onSelectLocation("recommended") }
                    .padding(12.dp),
                verticalArrangement = Arrangement.spacedBy(3.dp)
            ) {
                Text(
                    text = "Рекомендуемый",
                    color = Color.White,
                    fontSize = 14.sp,
                    fontWeight = FontWeight.Bold
                )
                Text(
                    text = "Anycast (REC)",
                    color = Color(0xFF94A3B8),
                    fontSize = 12.sp
                )
            }

            Column(
                modifier = Modifier
                    .weight(1f)
                    .clip(RoundedCornerShape(14.dp))
                    .background(
                        if (isFastest) Color(0xFF4F46E5).copy(alpha = 0.2f)
                        else Color(0xFF060911).copy(alpha = 0.6f)
                    )
                    .border(
                        BorderStroke(
                            1.dp,
                            if (isFastest) Color(0xFF6366F1)
                            else Color(0xFF1E293B)
                        ),
                        RoundedCornerShape(14.dp)
                    )
                    .clickable { onSelectLocation("fastest") }
                    .padding(12.dp),
                verticalArrangement = Arrangement.spacedBy(3.dp)
            ) {
                Text(
                    text = "⚡ Быстрейший",
                    color = Color.White,
                    fontSize = 14.sp,
                    fontWeight = FontWeight.Bold
                )
                Text(
                    text = "Мин. задержка",
                    color = Color(0xFF94A3B8),
                    fontSize = 12.sp
                )
            }
        }

        // Scrollable 32-Country List
        LazyColumn(
            modifier = Modifier
                .weight(1f)
                .fillMaxWidth()
                .clip(RoundedCornerShape(16.dp))
                .background(Color(0xFF060911).copy(alpha = 0.6f))
                .border(BorderStroke(1.dp, Color(0xFF1E293B)), RoundedCornerShape(16.dp))
        ) {
            itemsIndexed(filteredLocations, key = { _, it -> it.code }) { idx, loc ->
                val isSelected = snap.selectedLocation.equals(loc.code, ignoreCase = true)
                val latencyColor = when {
                    loc.latencyMs < 45 -> Color(0xFF34D399)
                    loc.latencyMs < 110 -> Color(0xFFFBBF24)
                    else -> Color(0xFF94A3B8)
                }

                Column {
                    Row(
                        modifier = Modifier
                            .fillMaxWidth()
                            .background(
                                if (isSelected) Color(0xFF4F46E5).copy(alpha = 0.15f)
                                else Color.Transparent
                            )
                            .clickable { onSelectLocation(loc.code) }
                            .padding(horizontal = 16.dp, vertical = 14.dp),
                        horizontalArrangement = Arrangement.SpaceBetween,
                        verticalAlignment = Alignment.CenterVertically
                    ) {
                        Row(
                            verticalAlignment = Alignment.CenterVertically,
                            horizontalArrangement = Arrangement.spacedBy(12.dp),
                            modifier = Modifier.weight(1f)
                        ) {
                            Text(text = loc.flag, fontSize = 22.sp)
                            Column(verticalArrangement = Arrangement.spacedBy(2.dp)) {
                                Text(
                                    text = loc.name,
                                    color = Color.White,
                                    fontSize = 15.sp,
                                    fontWeight = FontWeight.SemiBold
                                )
                                Text(
                                    text = "${loc.city} · ${loc.host}",
                                    color = Color(0xFF94A3B8),
                                    fontSize = 12.sp,
                                    fontFamily = FontFamily.Monospace,
                                    maxLines = 1,
                                    overflow = TextOverflow.Ellipsis
                                )
                            }
                        }

                        Spacer(modifier = Modifier.width(8.dp))

                        Text(
                            text = "${loc.latencyMs} мс",
                            color = latencyColor,
                            fontSize = 13.sp,
                            fontFamily = FontFamily.Monospace,
                            fontWeight = FontWeight.SemiBold
                        )
                    }

                    if (idx < filteredLocations.size - 1) {
                        HorizontalDivider(color = Color(0xFF1E293B).copy(alpha = 0.7f), thickness = 1.dp)
                    }
                }
            }
        }
    }
}

// ============================================================================
// BUILT-IN MOZILLA FXA WEBVIEW DIALOG (WebChannelMessageToChrome)
// ============================================================================
@SuppressLint("SetJavaScriptEnabled")
@Composable
private fun FxaWebViewDialog(
    initialEmail: String,
    onDismiss: () -> Unit,
    onSessionCaptured: (email: String, sessionToken: String) -> Unit
) {
    val fxaUrl = remember(initialEmail) {
        val base = "https://accounts.firefox.com/?context=fx_desktop_v3&service=sync&action=email"
        if (initialEmail.isNotBlank()) "$base&email=${android.net.Uri.encode(initialEmail)}" else base
    }

    Dialog(
        onDismissRequest = onDismiss,
        properties = DialogProperties(usePlatformDefaultWidth = false)
    ) {
        Column(
            modifier = Modifier
                .fillMaxSize()
                .background(Color(0xFF090D16))
                .statusBarsPadding()
                .navigationBarsPadding()
        ) {
            Row(
                modifier = Modifier
                    .fillMaxWidth()
                    .background(Color(0xFF060911))
                    .padding(horizontal = 16.dp, vertical = 12.dp),
                horizontalArrangement = Arrangement.SpaceBetween,
                verticalAlignment = Alignment.CenterVertically
            ) {
                Column {
                    Text(
                        text = "Вход в аккаунт Mozilla",
                        color = Color.White,
                        fontSize = 15.sp,
                        fontWeight = FontWeight.Bold
                    )
                    Text(
                        text = "accounts.firefox.com",
                        color = Color(0xFF94A3B8),
                        fontSize = 12.sp,
                        fontFamily = FontFamily.Monospace
                    )
                }

                TextButton(onClick = onDismiss) {
                    Text("Закрыть", color = Color(0xFF818CF8), fontSize = 14.sp, fontWeight = FontWeight.SemiBold)
                }
            }

            AndroidView(
                modifier = Modifier
                    .weight(1f)
                    .fillMaxWidth(),
                factory = { ctx ->
                    WebView(ctx).apply {
                        CookieManager.getInstance().setAcceptCookie(true)
                        CookieManager.getInstance().setAcceptThirdPartyCookies(this, true)
                        settings.javaScriptEnabled = true
                        settings.domStorageEnabled = true
                        settings.userAgentString =
                            "Mozilla/5.0 (Windows NT 10.0; Win64; x64; rv:148.0) Gecko/20100101 Firefox/148.0"

                        addJavascriptInterface(
                            object {
                                @JavascriptInterface
                                fun onFxaLogin(email: String, sessionToken: String) {
                                    if (sessionToken.length >= 32) {
                                        post {
                                            onSessionCaptured(email, sessionToken)
                                        }
                                    }
                                }
                            },
                            "MozillaVpnBridge"
                        )

                        val injectScript = """
                            (function() {
                                if (window.__mozVpnHooked) return;
                                window.__mozVpnHooked = true;
                                window.addEventListener('WebChannelMessageToChrome', function(e) {
                                    try {
                                        var detail = e.detail;
                                        if (typeof detail === 'string') {
                                            detail = JSON.parse(detail);
                                        }
                                        if (!detail || detail.id !== 'account_updates') return;
                                        var msg = detail.message || {};
                                        var cmd = msg.command;
                                        var data = msg.data || {};
                                        var msgId = msg.messageId;
                                        if (cmd === 'fxaccounts:fxa_status') {
                                            window.dispatchEvent(new CustomEvent('WebChannelMessageToContent', {
                                                detail: JSON.stringify({
                                                    id: 'account_updates',
                                                    messageId: msgId,
                                                    message: {
                                                        command: 'fxaccounts:fxa_status',
                                                        data: {
                                                            signedInUser: null,
                                                            clientId: '5882386c6d801776',
                                                            capabilities: { engines: ['bookmarks', 'history'] }
                                                        }
                                                    }
                                                })
                                            }));
                                        } else if (cmd === 'fxaccounts:can_link_account') {
                                            window.dispatchEvent(new CustomEvent('WebChannelMessageToContent', {
                                                detail: JSON.stringify({
                                                    id: 'account_updates',
                                                    messageId: msgId,
                                                    message: {
                                                        command: 'fxaccounts:can_link_account',
                                                        data: { ok: true }
                                                    }
                                                })
                                            }));
                                        } else if (cmd === 'fxaccounts:login' || cmd === 'fxaccounts:oauth_login') {
                                            window.dispatchEvent(new CustomEvent('WebChannelMessageToContent', {
                                                detail: JSON.stringify({
                                                    id: 'account_updates',
                                                    messageId: msgId,
                                                    message: {
                                                        command: cmd,
                                                        data: { ok: true }
                                                    }
                                                })
                                            }));
                                            if (data.sessionToken && window.MozillaVpnBridge) {
                                                window.MozillaVpnBridge.onFxaLogin(data.email || '', data.sessionToken);
                                            }
                                        }
                                    } catch (err) {}
                                }, true);
                            })();
                        """.trimIndent()

                        webViewClient = object : WebViewClient() {
                            override fun onPageStarted(view: WebView?, url: String?, favicon: android.graphics.Bitmap?) {
                                super.onPageStarted(view, url, favicon)
                                view?.evaluateJavascript(injectScript, null)
                            }

                            override fun onPageFinished(view: WebView?, url: String?) {
                                super.onPageFinished(view, url)
                                view?.evaluateJavascript(injectScript, null)
                            }

                            override fun shouldOverrideUrlLoading(
                                view: WebView?,
                                request: WebResourceRequest?
                            ): Boolean = false
                        }

                        loadUrl(fxaUrl)
                    }
                }
            )
        }
    }
}

// ============================================================================
// CUSTOM VECTOR CANVAS ICONS
// ============================================================================
@Composable
private fun PowerIcon(color: Color, size: Dp) {
    Canvas(modifier = Modifier.size(size)) {
        val stroke = size.toPx() * 0.10f
        val r = (this.size.minDimension - stroke) * 0.42f
        val center = Offset(this.size.width / 2f, this.size.height / 2f)
        drawArc(
            color = color,
            startAngle = -55f,
            sweepAngle = 290f,
            useCenter = false,
            topLeft = Offset(center.x - r, center.y - r),
            size = Size(r * 2f, r * 2f),
            style = Stroke(width = stroke, cap = StrokeCap.Round)
        )
        drawLine(
            color = color,
            start = Offset(center.x, center.y - r * 1.05f),
            end = Offset(center.x, center.y - r * 0.15f),
            strokeWidth = stroke,
            cap = StrokeCap.Round
        )
    }
}

@Composable
private fun ShieldIcon(color: Color, size: Dp) {
    Canvas(modifier = Modifier.size(size)) {
        val stroke = size.toPx() * 0.12f
        val w = this.size.width
        val h = this.size.height
        val path = androidx.compose.ui.graphics.Path().apply {
            moveTo(w * 0.5f, h * 0.1f)
            lineTo(w * 0.85f, h * 0.25f)
            lineTo(w * 0.85f, h * 0.55f)
            quadraticTo(w * 0.85f, h * 0.82f, w * 0.5f, h * 0.92f)
            quadraticTo(w * 0.15f, h * 0.82f, w * 0.15f, h * 0.55f)
            lineTo(w * 0.15f, h * 0.25f)
            close()
        }
        drawPath(path = path, color = color, style = Stroke(width = stroke, cap = StrokeCap.Round))
    }
}

@Composable
private fun UsersIcon(color: Color, size: Dp) {
    Canvas(modifier = Modifier.size(size)) {
        val stroke = size.toPx() * 0.11f
        val w = this.size.width
        val h = this.size.height
        drawCircle(
            color = color,
            radius = w * 0.18f,
            center = Offset(w * 0.5f, h * 0.34f),
            style = Stroke(width = stroke)
        )
        drawArc(
            color = color,
            startAngle = 200f,
            sweepAngle = 140f,
            useCenter = false,
            topLeft = Offset(w * 0.2f, h * 0.55f),
            size = Size(w * 0.6f, h * 0.45f),
            style = Stroke(width = stroke, cap = StrokeCap.Round)
        )
    }
}

@Composable
private fun GlobeIcon(color: Color, size: Dp) {
    Canvas(modifier = Modifier.size(size)) {
        val stroke = size.toPx() * 0.10f
        val r = (this.size.minDimension - stroke) * 0.42f
        val center = Offset(this.size.width / 2f, this.size.height / 2f)
        drawCircle(
            color = color,
            radius = r,
            center = center,
            style = Stroke(width = stroke)
        )
        drawLine(
            color = color,
            start = Offset(center.x - r, center.y),
            end = Offset(center.x + r, center.y),
            strokeWidth = stroke
        )
        drawOval(
            color = color,
            topLeft = Offset(center.x - r * 0.48f, center.y - r),
            size = Size(r * 0.96f, r * 2f),
            style = Stroke(width = stroke)
        )
    }
}
