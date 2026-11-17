package app.mozillavpnlite

import android.annotation.SuppressLint
import android.app.Activity
import android.content.Context
import android.graphics.Typeface
import android.net.VpnService
import android.os.Bundle
import android.webkit.CookieManager
import android.webkit.JavascriptInterface
import android.webkit.WebResourceRequest
import android.webkit.WebView
import android.webkit.WebViewClient
import androidx.activity.ComponentActivity
import androidx.activity.SystemBarStyle
import androidx.activity.compose.BackHandler
import androidx.activity.compose.rememberLauncherForActivityResult
import androidx.activity.compose.setContent
import androidx.activity.enableEdgeToEdge
import androidx.activity.result.contract.ActivityResultContracts
import androidx.compose.animation.core.animateDpAsState
import androidx.compose.animation.core.tween
import androidx.compose.foundation.BorderStroke
import androidx.compose.foundation.Canvas
import androidx.compose.foundation.background
import androidx.compose.foundation.border
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.*
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import androidx.compose.foundation.lazy.itemsIndexed
import androidx.compose.foundation.text.BasicTextField
import androidx.compose.foundation.text.KeyboardOptions
import androidx.compose.material3.*
import androidx.compose.runtime.*
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.draw.alpha
import androidx.compose.ui.focus.FocusRequester
import androidx.compose.ui.focus.focusRequester
import androidx.compose.ui.focus.onFocusChanged
import androidx.compose.ui.geometry.Offset
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.PathEffect
import androidx.compose.ui.graphics.RectangleShape
import androidx.compose.ui.graphics.SolidColor
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.text.TextStyle
import androidx.compose.ui.text.font.FontFamily
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.input.ImeAction
import androidx.compose.ui.text.input.KeyboardType
import androidx.compose.ui.text.input.PasswordVisualTransformation
import androidx.compose.ui.text.input.VisualTransformation
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import androidx.compose.ui.viewinterop.AndroidView
import androidx.compose.ui.window.Dialog
import androidx.compose.ui.window.DialogProperties
import kotlinx.coroutines.delay
import kotlinx.coroutines.launch
import kotlin.math.roundToInt

// ============================================================================
// ДИЗАЙН «ПРИБОР» — точный инструмент / заводской шильдик, а не «тёмный VPN».
// Бумага + чернила + один сигнальный оранжевый. Никаких карточек со
// скруглениями, градиентов и круглых кнопок: волосяные линии, таблицы с
// точечными лидерами, рубильник вместо кнопки питания.
//
// Шрифты (скачиваются Gradle-задачей fetchDesignFonts в assets/fonts,
// в репозитории бинарники не хранятся):
//  - Дисплейный: Russo One — кириллица родная, характер технического плаката.
//    (Zilla Slab от Mozilla кириллицы не содержит вообще — проверено по cmap.)
//  - Данные: IBM Plex Mono (Regular/Medium/SemiBold), табличные цифры.
// ============================================================================

private val Paper = Color(0xFFF4F1EA)
private val PaperDeep = Color(0xFFEBE5D8)
private val Ink = Color(0xFF17130C)
private val InkSoft = Color(0xFF554E40)
private val Signal = Color(0xFFFF4D00)
private val Alarm = Color(0xFFB3261E)

private object PriborFonts {
    var display: FontFamily = FontFamily.Default
        private set
    private var monoMedium: FontFamily = FontFamily.Monospace
    private var monoSemiBold: FontFamily = FontFamily.Monospace
    private var monoBold: FontFamily = FontFamily.Monospace

    fun init(context: Context) {
        display = load(context, "fonts/RussoOne-Regular.ttf", FontFamily.Default)
        monoMedium = load(context, "fonts/IBMPlexMono-Medium.ttf", FontFamily.Monospace)
        monoSemiBold = load(context, "fonts/IBMPlexMono-SemiBold.ttf", FontFamily.Monospace)
        monoBold = load(context, "fonts/IBMPlexMono-Bold.ttf", FontFamily.Monospace)
    }

    private fun load(context: Context, path: String, fallback: FontFamily): FontFamily =
        try {
            FontFamily(Typeface.createFromAsset(context.assets, path))
        } catch (e: Exception) {
            fallback
        }

    // Веса подняты на ступень относительно названий: обычный текст рисуется
    // Medium, средний — SemiBold, акценты — Bold. Regular у Plex Mono на
    // мелких кеглях читается тонко (замечание Рината по первому скриншоту).
    fun mono(weight: FontWeight): FontFamily = when (weight) {
        FontWeight.SemiBold, FontWeight.Bold -> monoBold
        FontWeight.Medium -> monoSemiBold
        else -> monoMedium
    }
}

class MainActivity : ComponentActivity() {
    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        enableEdgeToEdge(
            statusBarStyle = SystemBarStyle.light(
                android.graphics.Color.TRANSPARENT,
                android.graphics.Color.TRANSPARENT
            ),
            navigationBarStyle = SystemBarStyle.light(
                android.graphics.Color.TRANSPARENT,
                android.graphics.Color.TRANSPARENT
            )
        )
        CoreBridge.init(this)
        PriborFonts.init(this)

        if (android.os.Build.VERSION.SDK_INT >= 33) {
            if (checkSelfPermission("android.permission.POST_NOTIFICATIONS") != android.content.pm.PackageManager.PERMISSION_GRANTED) {
                requestPermissions(arrayOf("android.permission.POST_NOTIFICATIONS"), 101)
            }
        }

        setContent {
            MaterialTheme(
                colorScheme = lightColorScheme(
                    primary = Ink,
                    background = Paper,
                    surface = Paper,
                    onBackground = Ink,
                    onSurface = Ink
                )
            ) {
                Surface(
                    modifier = Modifier.fillMaxSize(),
                    color = Paper
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
    // Hoisted for use inside non-composable lambdas below.
    val msgAddAccountFirst = stringResource(R.string.add_account_first)
    val msgCheckingSession = stringResource(R.string.checking_session)

    var snap by remember { mutableStateOf(CoreSnapshot()) }
    var tab by remember { mutableIntStateOf(0) } // 0 = Пульт, 1 = Аккаунты, 2 = Серверы
    var showHomePicker by remember { mutableStateOf(false) }
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

    var isAppVisible by remember { mutableStateOf(true) }
    DisposableEffect(context) {
        val owner = context as? androidx.lifecycle.LifecycleOwner
        val observer = androidx.lifecycle.LifecycleEventObserver { _, event ->
            when (event) {
                androidx.lifecycle.Lifecycle.Event.ON_START -> isAppVisible = true
                androidx.lifecycle.Lifecycle.Event.ON_STOP -> isAppVisible = false
                else -> {}
            }
        }
        owner?.lifecycle?.addObserver(observer)
        onDispose {
            owner?.lifecycle?.removeObserver(observer)
        }
    }

    LaunchedEffect(isAppVisible) {
        if (!isAppVisible) return@LaunchedEffect
        CoreBridge.refreshServers()
        snap = CoreBridge.getSnapshot()
        while (isAppVisible) {
            delay(1500)
            snap = CoreBridge.getSnapshot()
        }
    }

    val recommendedName = stringResource(R.string.mode_recommended)
    val activeLocation: LocationItem? = remember(snap) {
        val locs = snap.locations
        when (snap.selectedLocation) {
            // Recommended = Mozilla's anycast endpoint (host is a stable
            // Mozilla constant; the core resolves the actual connection).
            "recommended" -> LocationItem(
                code = "REC",
                flag = "",
                name = recommendedName,
                city = "Anycast",
                host = "p.m1.fastly-masque.net",
                port = 2499,
                latencyMs = 0
            )
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
                authMessage = msgAddAccountFirst
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
            .background(Paper)
            .statusBarsPadding()
            .navigationBarsPadding()
    ) {
        // Шильдик: имя прибора слева, состояние — справа.
        Row(
            modifier = Modifier
                .fillMaxWidth()
                .height(52.dp)
                .padding(horizontal = 20.dp),
            verticalAlignment = Alignment.CenterVertically,
            horizontalArrangement = Arrangement.SpaceBetween
        ) {
            Row(
                verticalAlignment = Alignment.CenterVertically,
                horizontalArrangement = Arrangement.spacedBy(9.dp)
            ) {
                Box(
                    modifier = Modifier
                        .size(9.dp)
                        .background(if (snap.state == "connected") Signal else Ink)
                )
                Text(
                    text = stringResource(R.string.brand_plate),
                    color = Ink,
                    fontSize = 14.sp,
                    fontFamily = PriborFonts.mono(FontWeight.SemiBold),
                    fontWeight = FontWeight.SemiBold
                )
            }

            if (snap.state == "connected" && activeLocation != null) {
                Text(
                    text = if (snap.selectedLocation == "recommended") "REC · Anycast"
                        else "${activeLocation.code} · ${CoreBridge.formatPing(activeLocation.latencyMs)}",
                    color = Ink,
                    fontSize = 14.sp,
                    fontFamily = PriborFonts.mono(FontWeight.SemiBold),
                    fontWeight = FontWeight.SemiBold
                )
            } else {
                Text(
                    text = stringResource(R.string.state_off),
                    color = InkSoft,
                    fontSize = 14.sp,
                    fontFamily = PriborFonts.mono(FontWeight.Normal)
                )
            }
        }

        Box(
            modifier = Modifier
                .fillMaxWidth()
                .height(2.dp)
                .background(Ink)
        )

        BackHandler(enabled = showHomePicker) { showHomePicker = false }

        // Main Screen Content
        Box(
            modifier = Modifier
                .weight(1f)
                .fillMaxWidth()
        ) {
            if (showHomePicker) {
                HomeCountryPickerScreen(
                    snap = snap,
                    onPick = { code ->
                        scope.launch {
                            CoreBridge.setHomeCountry(code)
                            snap = CoreBridge.getSnapshot()
                        }
                        showHomePicker = false
                    }
                )
            } else when (tab) {
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
                    },
                    onBypassChange = { enabled ->
                        scope.launch {
                            CoreBridge.setBypassEnabled(enabled)
                            snap = CoreBridge.getSnapshot()
                        }
                    },
                    onOpenHomePicker = { showHomePicker = true }
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

        Box(
            modifier = Modifier
                .fillMaxWidth()
                .height(2.dp)
                .background(Ink)
        )

        // Нижняя навигация: три подписи в общей рамке, активная — с оранжевым квадратом.
        Row(
            modifier = Modifier
                .fillMaxWidth()
                .height(60.dp)
                .background(Paper),
            verticalAlignment = Alignment.CenterVertically
        ) {
            BottomNavTabItem(
                selected = tab == 0,
                label = stringResource(R.string.tab_console),
                onClick = { tab = 0; showHomePicker = false },
                modifier = Modifier.weight(1f)
            )
            BottomNavTabItem(
                selected = tab == 1,
                label = stringResource(R.string.tab_accounts, snap.accounts.size),
                onClick = { tab = 1; showHomePicker = false },
                modifier = Modifier.weight(1f)
            )
            BottomNavTabItem(
                selected = tab == 2,
                label = stringResource(R.string.tab_servers, snap.locations.size),
                onClick = { tab = 2; showHomePicker = false },
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
                    authMessage = msgCheckingSession
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
    onClick: () -> Unit,
    modifier: Modifier = Modifier
) {
    Row(
        modifier = modifier
            .fillMaxHeight()
            .clickable(onClick = onClick),
        horizontalArrangement = Arrangement.Center,
        verticalAlignment = Alignment.CenterVertically
    ) {
        if (selected) {
            Box(
                modifier = Modifier
                    .size(8.dp)
                    .background(Signal)
            )
            Spacer(modifier = Modifier.width(8.dp))
        }
        Text(
            text = label,
            color = if (selected) Ink else InkSoft,
            fontSize = 13.sp,
            fontFamily = PriborFonts.mono(if (selected) FontWeight.SemiBold else FontWeight.Normal),
            fontWeight = if (selected) FontWeight.SemiBold else FontWeight.Normal,
            maxLines = 1,
            overflow = TextOverflow.Ellipsis
        )
    }
}

// ============================================================================
// ПРИМИТИВЫ «ПРИБОРА»
// ============================================================================

/** Подпись секции: моно-подпись + волосяная линия. */
@Composable
private fun SectionLabel(text: String) {
    Column(verticalArrangement = Arrangement.spacedBy(8.dp)) {
        Text(
            text = text,
            color = InkSoft,
            fontSize = 12.sp,
            fontFamily = PriborFonts.mono(FontWeight.SemiBold),
            fontWeight = FontWeight.SemiBold,
            letterSpacing = 2.sp
        )
        Box(
            modifier = Modifier
                .fillMaxWidth()
                .height(1.dp)
                .background(Ink)
        )
    }
}

/** Строка спецификации: ПОДПИСЬ ········ ЗНАЧЕНИЕ. */
@Composable
private fun SpecRow(label: String, value: String, valueColor: Color = Ink) {
    Row(
        modifier = Modifier.fillMaxWidth(),
        verticalAlignment = Alignment.Bottom
    ) {
        Text(
            text = label,
            color = InkSoft,
            fontSize = 13.sp,
            fontFamily = PriborFonts.mono(FontWeight.Normal)
        )
        Canvas(
            modifier = Modifier
                .weight(1f)
                .height(13.dp)
                .padding(horizontal = 8.dp)
        ) {
            drawLine(
                color = InkSoft,
                start = Offset(0f, size.height - 3f),
                end = Offset(size.width, size.height - 3f),
                strokeWidth = 1.5f,
                pathEffect = PathEffect.dashPathEffect(floatArrayOf(1f, 5f))
            )
        }
        Text(
            text = value,
            color = valueColor,
            fontSize = 14.sp,
            fontFamily = PriborFonts.mono(FontWeight.SemiBold),
            fontWeight = FontWeight.SemiBold
        )
    }
}

/** Квота сегментным «барабаном»: 14 ячеек; любой ненулевой расход зажигает хотя бы одну. */
@Composable
private fun QuotaDrum(fraction: Double, alarm: Boolean) {
    val segments = 14
    val filled = if (fraction <= 0.0) 0
        else kotlin.math.ceil(fraction * segments).toInt().coerceIn(1, segments)
    Row(
        modifier = Modifier.fillMaxWidth(),
        horizontalArrangement = Arrangement.spacedBy(3.dp)
    ) {
        repeat(segments) { i ->
            val isFilled = i < filled
            Box(
                modifier = Modifier
                    .weight(1f)
                    .height(14.dp)
                    .border(BorderStroke(1.dp, Ink), RectangleShape)
                    .background(
                        when {
                            !isFilled -> Paper
                            alarm -> Signal
                            else -> Ink
                        }
                    )
            )
        }
    }
}

/**
 * Рубильник как механизм, а не сегментная кнопка: пластина, в ней канал,
 * по каналу ходит выступающая рукоять с насечками. Подписи ВЫКЛ/ВКЛ стоят
 * на пластине по бокам канала и не уезжают под ползун. При ВКЛ канал
 * заливается сигнальным оранжевым — «сеть под напряжением». Движение
 * ступенчатое, без пружин (tween 150 мс).
 */
/** Мини-рубильник для карточки «Домашние сайты»: та же геометрия, что у
 * главного, — оранжевый сегмент и чёрная рукоять с насечками. */
@Composable
private fun MiniBreakerToggle(checked: Boolean, onClick: () -> Unit) {
    @Composable
    fun grip() {
        Box(
            modifier = Modifier
                .width(22.dp)
                .fillMaxHeight()
                .background(Ink),
            contentAlignment = Alignment.Center
        ) {
            Row(horizontalArrangement = Arrangement.spacedBy(4.dp)) {
                repeat(2) {
                    Box(
                        modifier = Modifier
                            .width(2.5.dp)
                            .height(12.dp)
                            .background(Paper)
                    )
                }
            }
        }
    }
    Row(
        modifier = Modifier
            .width(56.dp)
            .height(30.dp)
            .border(BorderStroke(2.dp, Ink), RectangleShape)
            .clickable { onClick() }
    ) {
        if (checked) {
            Box(
                modifier = Modifier
                    .width(30.dp)
                    .fillMaxHeight()
                    .background(Signal)
            )
            grip()
        } else {
            grip()
            Box(
                modifier = Modifier
                    .weight(1f)
                    .fillMaxHeight()
                    .background(PaperDeep)
            )
        }
    }
}

/** Выбор «своей страны» для обхода туннеля — экран в стиле вкладки
 * «Серверы»: заголовок, поиск, список; выбранная строка подсвечена. */
@Composable
private fun HomeCountryPickerScreen(
    snap: CoreSnapshot,
    onPick: (String) -> Unit
) {
    var query by remember { mutableStateOf("") }
    val all = remember { CoreBridge.homeCountries() }
    val sorted = remember(all, snap.homeCountry) {
        val sel = all.filter { it.code.equals(snap.homeCountry, ignoreCase = true) }
        val rest = all.filter { !it.code.equals(snap.homeCountry, ignoreCase = true) }
            .sortedBy { it.name }
        sel + rest
    }
    val shown = if (query.isBlank()) sorted else sorted.filter {
        it.name.contains(query, ignoreCase = true) ||
            it.code.contains(query, ignoreCase = true)
    }
    LazyColumn(
        modifier = Modifier
            .fillMaxSize()
            .padding(horizontal = 20.dp),
        contentPadding = PaddingValues(vertical = 20.dp),
        verticalArrangement = Arrangement.spacedBy(10.dp)
    ) {
        item {
            Column(verticalArrangement = Arrangement.spacedBy(10.dp)) {
                Text(
                    text = stringResource(R.string.your_country),
                    color = Ink,
                    fontSize = 38.sp,
                    fontFamily = PriborFonts.display,
                    maxLines = 1
                )
                Text(
                    text = stringResource(R.string.picker_subtitle),
                    color = InkSoft,
                    fontSize = 11.sp,
                    fontFamily = PriborFonts.mono(FontWeight.SemiBold),
                    fontWeight = FontWeight.SemiBold,
                    letterSpacing = 1.5.sp
                )
                Text(
                    text = stringResource(R.string.picker_source),
                    color = InkSoft,
                    fontSize = 11.sp,
                    fontFamily = PriborFonts.mono(FontWeight.SemiBold),
                    fontWeight = FontWeight.SemiBold,
                    letterSpacing = 1.5.sp
                )
            }
        }
        item {
            Column {
                Text(
                    text = stringResource(R.string.search_label),
                    color = InkSoft,
                    fontSize = 12.sp,
                    fontFamily = PriborFonts.mono(FontWeight.SemiBold),
                    fontWeight = FontWeight.SemiBold,
                    letterSpacing = 2.sp
                )
                BasicTextField(
                    value = query,
                    onValueChange = { query = it },
                    singleLine = true,
                    textStyle = TextStyle(
                        color = Ink,
                        fontSize = 15.sp,
                        fontFamily = PriborFonts.mono(FontWeight.Normal)
                    ),
                    cursorBrush = SolidColor(Signal),
                    modifier = Modifier
                        .fillMaxWidth()
                        .padding(vertical = 8.dp),
                    decorationBox = { inner ->
                        Box {
                            if (query.isEmpty()) {
                                Text(
                                    text = stringResource(R.string.search_country_hint),
                                    color = InkSoft,
                                    fontSize = 15.sp,
                                    fontFamily = PriborFonts.mono(FontWeight.Normal)
                                )
                            }
                            inner()
                        }
                    }
                )
                Box(
                    modifier = Modifier
                        .fillMaxWidth()
                        .height(2.dp)
                        .background(Ink)
                )
            }
        }
        items(shown) { c ->
            val selected = c.code.equals(snap.homeCountry, ignoreCase = true)
            Row(
                modifier = Modifier
                    .fillMaxWidth()
                    .background(if (selected) PaperDeep else Paper)
                    .border(
                        BorderStroke(if (selected) 2.dp else 1.dp, Ink),
                        RectangleShape
                    )
                    .clickable { onPick(c.code) },
                verticalAlignment = Alignment.CenterVertically
            ) {
                Box(
                    modifier = Modifier
                        .width(6.dp)
                        .height(52.dp)
                        .background(if (selected) Signal else Color.Transparent)
                )
                Row(
                    modifier = Modifier.padding(horizontal = 12.dp, vertical = 14.dp),
                    verticalAlignment = Alignment.CenterVertically
                ) {
                    Text(
                        text = c.code,
                        color = Ink,
                        fontSize = 19.sp,
                        fontFamily = PriborFonts.display,
                        modifier = Modifier.width(52.dp)
                    )
                    Text(
                        text = c.name,
                        color = Ink,
                        fontSize = 14.sp,
                        fontFamily = PriborFonts.mono(FontWeight.SemiBold),
                        fontWeight = FontWeight.SemiBold
                    )
                }
            }
        }
    }
}

@Composable
private fun BreakerSwitch(
    isOn: Boolean,
    isConnecting: Boolean,
    onToggle: () -> Unit
) {
    Box(modifier = Modifier.fillMaxWidth()) {
        // Жёсткая тень-пластина.
        Box(
            modifier = Modifier
                .matchParentSize()
                .offset(x = 6.dp, y = 6.dp)
                .background(Ink)
        )
        Row(
            modifier = Modifier
                .fillMaxWidth()
                .background(Paper)
                .border(BorderStroke(2.dp, Ink), RectangleShape)
                .clickable(onClick = onToggle)
                .padding(horizontal = 14.dp, vertical = 13.dp),
            verticalAlignment = Alignment.CenterVertically,
            horizontalArrangement = Arrangement.spacedBy(13.dp)
        ) {
            Text(
                text = stringResource(R.string.breaker_off),
                color = if (!isOn && !isConnecting) Ink else InkSoft,
                fontSize = 16.sp,
                fontFamily = PriborFonts.display,
                letterSpacing = 1.sp
            )

            // Канал с рукоятью.
            BoxWithConstraints(
                modifier = Modifier
                    .weight(1f)
                    .height(46.dp)
                    .background(if (isOn) Signal else PaperDeep)
                    .border(BorderStroke(2.dp, Ink), RectangleShape)
            ) {
                val blockWidth = maxWidth * 0.38f
                val travel = maxWidth - blockWidth
                val target = when {
                    isConnecting -> travel / 2
                    isOn -> travel
                    else -> 0.dp
                }
                val blockOffset by animateDpAsState(
                    targetValue = target,
                    animationSpec = tween(durationMillis = 150),
                    label = "breakerBlock"
                )
                // Рукоять: выше канала на 8 дп, выступает над и под ним.
                Box(
                    modifier = Modifier
                        .width(blockWidth)
                        .height(54.dp)
                        .align(Alignment.CenterStart)
                        .offset(x = blockOffset)
                        .background(Ink)
                        .border(BorderStroke(2.dp, Ink), RectangleShape),
                    contentAlignment = Alignment.Center
                ) {
                    Row(horizontalArrangement = Arrangement.spacedBy(4.dp)) {
                        repeat(3) {
                            Box(
                                modifier = Modifier
                                    .width(3.dp)
                                    .height(26.dp)
                                    .background(Paper)
                            )
                        }
                    }
                }
            }

            Text(
                text = stringResource(R.string.breaker_on),
                color = if (isOn) Ink else InkSoft,
                fontSize = 16.sp,
                fontFamily = PriborFonts.display,
                letterSpacing = 1.sp
            )
        }
    }
}

/** Чёрная кнопка-пластина с жёсткой тенью. */
@Composable
private fun PlateButton(
    text: String,
    onClick: () -> Unit,
    modifier: Modifier = Modifier,
    enabled: Boolean = true,
    busy: Boolean = false
) {
    Box(modifier = modifier) {
        Box(
            modifier = Modifier
                .matchParentSize()
                .offset(x = 5.dp, y = 5.dp)
                .background(if (enabled) Ink else InkSoft)
        )
        Row(
            modifier = Modifier
                .fillMaxWidth()
                .height(52.dp)
                .background(if (enabled) Ink else InkSoft)
                .clickable(enabled = enabled, onClick = onClick)
                .padding(horizontal = 16.dp),
            horizontalArrangement = Arrangement.Center,
            verticalAlignment = Alignment.CenterVertically
        ) {
            if (busy) {
                CircularProgressIndicator(
                    modifier = Modifier.size(17.dp),
                    color = Paper,
                    strokeWidth = 2.dp
                )
                Spacer(modifier = Modifier.width(10.dp))
            }
            Text(
                text = text,
                color = Paper,
                fontSize = 14.sp,
                fontFamily = PriborFonts.mono(FontWeight.SemiBold),
                fontWeight = FontWeight.SemiBold,
                letterSpacing = 1.sp,
                maxLines = 1
            )
        }
    }
}

/** Вторичная кнопка: бумага в чёрной рамке. */
@Composable
private fun GhostButton(
    text: String,
    onClick: () -> Unit,
    modifier: Modifier = Modifier,
    enabled: Boolean = true
) {
    Box(
        modifier = modifier
            .height(52.dp)
            .background(Paper)
            .border(BorderStroke(2.dp, if (enabled) Ink else InkSoft), RectangleShape)
            .clickable(enabled = enabled, onClick = onClick)
            .padding(horizontal = 16.dp),
        contentAlignment = Alignment.Center
    ) {
        Text(
            text = text,
            color = if (enabled) Ink else InkSoft,
            fontSize = 14.sp,
            fontFamily = PriborFonts.mono(FontWeight.SemiBold),
            fontWeight = FontWeight.SemiBold,
            letterSpacing = 1.sp,
            maxLines = 1
        )
    }
}

/** Маленькая чёрная плашка-кнопка для шапок и карточек. */
@Composable
private fun SmallPlateButton(
    text: String,
    onClick: () -> Unit,
    modifier: Modifier = Modifier,
    enabled: Boolean = true,
    busy: Boolean = false
) {
    Row(
        modifier = modifier
            .background(if (enabled) Ink else InkSoft)
            .clickable(enabled = enabled, onClick = onClick)
            .padding(horizontal = 13.dp, vertical = 9.dp),
        verticalAlignment = Alignment.CenterVertically,
        horizontalArrangement = Arrangement.spacedBy(7.dp)
    ) {
        if (busy) {
            CircularProgressIndicator(
                modifier = Modifier.size(13.dp),
                color = Paper,
                strokeWidth = 2.dp
            )
        }
        Text(
            text = text,
            color = Paper,
            fontSize = 12.sp,
            fontFamily = PriborFonts.mono(FontWeight.SemiBold),
            fontWeight = FontWeight.SemiBold,
            letterSpacing = 1.sp,
            maxLines = 1
        )
    }
}

/** Поле бланка: подпись сверху, ввод над линейкой (в фокусе — оранжевой). */
@Composable
private fun FormField(
    label: String,
    value: String,
    onValueChange: (String) -> Unit,
    modifier: Modifier = Modifier,
    keyboardOptions: KeyboardOptions = KeyboardOptions.Default,
    visualTransformation: VisualTransformation = VisualTransformation.None,
    trailingText: String? = null,
    onTrailingClick: (() -> Unit)? = null,
    placeholder: String? = null
) {
    var focused by remember { mutableStateOf(false) }
    Column(
        modifier = modifier,
        verticalArrangement = Arrangement.spacedBy(6.dp)
    ) {
        Row(
            modifier = Modifier.fillMaxWidth(),
            horizontalArrangement = Arrangement.SpaceBetween,
            verticalAlignment = Alignment.Bottom
        ) {
            Text(
                text = label,
                color = InkSoft,
                fontSize = 11.sp,
                fontFamily = PriborFonts.mono(FontWeight.SemiBold),
                fontWeight = FontWeight.SemiBold,
                letterSpacing = 1.5.sp
            )
            if (trailingText != null && onTrailingClick != null) {
                Text(
                    text = trailingText,
                    color = Ink,
                    fontSize = 12.sp,
                    fontFamily = PriborFonts.mono(FontWeight.SemiBold),
                    fontWeight = FontWeight.SemiBold,
                    modifier = Modifier
                        .clickable(onClick = onTrailingClick)
                        .padding(start = 8.dp, bottom = 1.dp)
                )
            }
        }
        BasicTextField(
            value = value,
            onValueChange = onValueChange,
            singleLine = true,
            keyboardOptions = keyboardOptions,
            visualTransformation = visualTransformation,
            textStyle = TextStyle(
                color = Ink,
                fontSize = 15.sp,
                fontFamily = PriborFonts.mono(FontWeight.Medium),
                fontWeight = FontWeight.Medium
            ),
            cursorBrush = SolidColor(Signal),
            modifier = Modifier
                .fillMaxWidth()
                .onFocusChanged { focused = it.isFocused },
            decorationBox = { inner ->
                Box(modifier = Modifier.fillMaxWidth()) {
                    if (value.isEmpty() && placeholder != null) {
                        Text(
                            text = placeholder,
                            color = InkSoft,
                            fontSize = 15.sp,
                            fontFamily = PriborFonts.mono(FontWeight.Normal)
                        )
                    }
                    inner()
                }
            }
        )
        Box(
            modifier = Modifier
                .fillMaxWidth()
                .height(2.dp)
                .background(if (focused) Signal else Ink)
        )
    }
}

/**
 * Код клетками бланка: поверх невидимого поля ввода рисуется ряд ячеек.
 * Работает и для 6-значного кода из письма, и для 8-символьной разблокировки.
 */
@Composable
private fun CodeCells(
    label: String,
    value: String,
    length: Int,
    digitsOnly: Boolean,
    onValueChange: (String) -> Unit
) {
    val focusRequester = remember { FocusRequester() }
    var focused by remember { mutableStateOf(false) }
    Column(verticalArrangement = Arrangement.spacedBy(6.dp)) {
        Text(
            text = label,
            color = InkSoft,
            fontSize = 11.sp,
            fontFamily = PriborFonts.mono(FontWeight.SemiBold),
            fontWeight = FontWeight.SemiBold,
            letterSpacing = 1.5.sp
        )
        Box(
            modifier = Modifier
                .fillMaxWidth()
                .clickable { focusRequester.requestFocus() }
        ) {
            Row(
                modifier = Modifier.fillMaxWidth(),
                horizontalArrangement = Arrangement.spacedBy(7.dp)
            ) {
                repeat(length) { i ->
                    val ch = value.getOrNull(i)
                    val isActiveCell = focused && i == value.length
                    Box(
                        modifier = Modifier
                            .weight(1f)
                            .aspectRatio(0.92f)
                            .background(Paper)
                            .border(
                                BorderStroke(
                                    if (isActiveCell) 2.dp else 1.5.dp,
                                    if (isActiveCell) Signal else Ink
                                ),
                                RectangleShape
                            ),
                        contentAlignment = Alignment.Center
                    ) {
                        if (ch != null) {
                            Text(
                                text = ch.uppercaseChar().toString(),
                                color = Ink,
                                fontSize = 19.sp,
                                fontFamily = PriborFonts.mono(FontWeight.SemiBold),
                                fontWeight = FontWeight.SemiBold
                            )
                        }
                    }
                }
            }
            BasicTextField(
                value = value,
                onValueChange = { raw ->
                    val cleaned = if (digitsOnly) raw.filter { it.isDigit() } else raw.filter { !it.isWhitespace() }
                    if (cleaned.length <= length) onValueChange(cleaned)
                },
                keyboardOptions = KeyboardOptions(
                    keyboardType = if (digitsOnly) KeyboardType.Number else KeyboardType.Text,
                    imeAction = ImeAction.Done
                ),
                textStyle = TextStyle(color = Color.Transparent, fontSize = 1.sp),
                cursorBrush = SolidColor(Color.Transparent),
                modifier = Modifier
                    .fillMaxWidth()
                    .alpha(0f)
                    .focusRequester(focusRequester)
                    .onFocusChanged { focused = it.isFocused }
            )
        }
    }
}

/** Сегментный переключатель в виде рубильной полосы (2..3 сегмента). */
@Composable
private fun SegmentedBreaker(
    options: List<String>,
    selectedIndex: Int,
    onSelect: (Int) -> Unit
) {
    Row(
        modifier = Modifier
            .fillMaxWidth()
            .border(BorderStroke(2.dp, Ink), RectangleShape)
            .background(Paper)
    ) {
        options.forEachIndexed { idx, title ->
            val selected = idx == selectedIndex
            Box(
                modifier = Modifier
                    .weight(1f)
                    .background(if (selected) Ink else Paper)
                    .clickable { onSelect(idx) }
                    .padding(vertical = 10.dp, horizontal = 4.dp),
                contentAlignment = Alignment.Center
            ) {
                Text(
                    text = title,
                    color = if (selected) Paper else InkSoft,
                    fontSize = 12.sp,
                    fontFamily = PriborFonts.mono(if (selected) FontWeight.SemiBold else FontWeight.Normal),
                    fontWeight = if (selected) FontWeight.SemiBold else FontWeight.Normal,
                    maxLines = 1,
                    overflow = TextOverflow.Ellipsis
                )
            }
            if (idx < options.size - 1) {
                Box(
                    modifier = Modifier
                        .width(2.dp)
                        .height(38.dp)
                        .background(Ink)
                )
            }
        }
    }
}

/** Строка табло локаций: КОД | название/узел | пинг. */
@Composable
private fun LocationRow(
    code: String,
    title: String,
    subtitle: String,
    pingText: String,
    pingColor: Color = Ink,
    selected: Boolean,
    onClick: () -> Unit
) {
    Row(
        modifier = Modifier
            .fillMaxWidth()
            .background(if (selected) PaperDeep else Paper)
            .clickable(onClick = onClick)
            .padding(end = 14.dp),
        verticalAlignment = Alignment.CenterVertically
    ) {
        Box(
            modifier = Modifier
                .width(6.dp)
                .height(56.dp)
                .background(if (selected) Signal else Color.Transparent)
        )
        Spacer(modifier = Modifier.width(10.dp))
        Text(
            text = code,
            color = Ink,
            fontSize = 18.sp,
            fontFamily = PriborFonts.display,
            modifier = Modifier.width(68.dp),
            maxLines = 1
        )
        Column(
            modifier = Modifier.weight(1f),
            verticalArrangement = Arrangement.spacedBy(2.dp)
        ) {
            Text(
                text = title,
                color = Ink,
                fontSize = 14.sp,
                fontFamily = PriborFonts.mono(FontWeight.SemiBold),
                fontWeight = FontWeight.SemiBold,
                maxLines = 1,
                overflow = TextOverflow.Ellipsis
            )
            Text(
                text = subtitle,
                color = InkSoft,
                fontSize = 11.sp,
                fontFamily = PriborFonts.mono(FontWeight.Normal),
                maxLines = 1,
                overflow = TextOverflow.Ellipsis
            )
        }
        Text(
            text = pingText,
            color = pingColor,
            fontSize = 13.sp,
            fontFamily = PriborFonts.mono(FontWeight.SemiBold),
            fontWeight = FontWeight.SemiBold
        )
    }
}

/** Плашка аккаунта для главного экрана и вкладки аккаунтов (общая часть). */
@Composable
private fun AccountPlateHeader(acc: AccountItem, isActive: Boolean) {
    Column(verticalArrangement = Arrangement.spacedBy(3.dp)) {
    Row(
        modifier = Modifier.fillMaxWidth(),
        verticalAlignment = Alignment.CenterVertically,
        horizontalArrangement = Arrangement.spacedBy(8.dp)
    ) {
        if (isActive) {
            Box(
                modifier = Modifier
                    .size(9.dp)
                    .background(Signal)
            )
        }
        Text(
            text = "#${acc.position}",
            color = InkSoft,
            fontSize = 13.sp,
            fontFamily = PriborFonts.mono(FontWeight.Normal)
        )
        Text(
            text = acc.email,
            color = Ink,
            fontSize = 14.sp,
            fontFamily = PriborFonts.mono(FontWeight.SemiBold),
            fontWeight = FontWeight.SemiBold,
            maxLines = 1,
            overflow = TextOverflow.Ellipsis,
            modifier = Modifier.weight(1f)
        )
        // A health verdict outranks the ACTIVE/READY labels: an active
        // but dead account used to show a clean «АКТИВЕН» while every
        // connection attempt died on it within a second.
        val healthRes = when (acc.status) {
            "reauth_required" -> R.string.acc_status_reauth
            "no_pass" -> R.string.acc_status_no_pass
            "not_entitled" -> R.string.acc_status_not_entitled
            "quota_exceeded" -> R.string.acc_status_exhausted
            "unreachable" -> R.string.acc_status_unreachable
            else -> null
        }
        Text(
            text = when {
                healthRes != null -> stringResource(healthRes)
                isActive -> stringResource(R.string.acc_status_active)
                acc.exhausted -> stringResource(R.string.acc_status_exhausted)
                else -> stringResource(R.string.acc_status_ready)
            },
            color = when {
                healthRes != null && acc.status != "unreachable" -> Alarm
                isActive -> Ink
                else -> InkSoft
            },
            fontSize = 11.sp,
            fontFamily = PriborFonts.mono(FontWeight.SemiBold),
            fontWeight = FontWeight.SemiBold
        )
    }
    val reasonText = acc.reason.ifBlank { acc.lastError }
    if (reasonText.isNotBlank()) {
        val checkedAt = CoreBridge.formatTimeOfDay(acc.lastCheckAtMs)
        Text(
            text = if (checkedAt.isNotBlank()) "$reasonText · $checkedAt" else reasonText,
            color = InkSoft,
            fontSize = 11.sp,
            fontFamily = PriborFonts.mono(FontWeight.Normal),
            maxLines = 2,
            overflow = TextOverflow.Ellipsis
        )
    }
    }
}

// ============================================================================
// TAB 1: ПУЛЬТ (HOME)
// ============================================================================
@Composable
private fun HomeTabScreen(
    snap: CoreSnapshot,
    activeLocation: LocationItem?,
    isConnecting: Boolean,
    onToggleTunnel: () -> Unit,
    onProtocolChange: (String) -> Unit,
    onSelectAccount: (Int) -> Unit,
    onSelectLocation: (String) -> Unit,
    onBypassChange: (Boolean) -> Unit,
    onOpenHomePicker: () -> Unit
) {
    val isConnected = snap.state == "connected"

    LazyColumn(
        modifier = Modifier
            .fillMaxSize()
            .padding(horizontal = 20.dp),
        contentPadding = PaddingValues(vertical = 20.dp),
        verticalArrangement = Arrangement.spacedBy(22.dp)
    ) {
        // 1. Состояние словом + спецификация + рубильник.
        item {
            Column(verticalArrangement = Arrangement.spacedBy(14.dp)) {
                Text(
                    text = when {
                        isConnecting -> stringResource(R.string.state_connecting)
                        isConnected -> stringResource(R.string.state_connected)
                        else -> stringResource(R.string.state_off)
                    },
                    color = Ink,
                    fontSize = 38.sp,
                    fontFamily = PriborFonts.display,
                    maxLines = 1
                )

                Row(
                    verticalAlignment = Alignment.CenterVertically,
                    horizontalArrangement = Arrangement.spacedBy(8.dp)
                ) {
                    Box(
                        modifier = Modifier
                            .size(9.dp)
                            .background(
                                when {
                                    isConnecting -> Ink
                                    isConnected -> Signal
                                    else -> InkSoft
                                }
                            )
                    )
                    Text(
                        text = when {
                            isConnecting -> stringResource(R.string.tunnel_connecting)
                            isConnected -> stringResource(R.string.tunnel_active)
                            else -> stringResource(R.string.tunnel_inactive)
                        },
                        color = InkSoft,
                        fontSize = 12.sp,
                        fontFamily = PriborFonts.mono(FontWeight.SemiBold),
                        fontWeight = FontWeight.SemiBold,
                        letterSpacing = 1.5.sp
                    )
                }

                if (activeLocation != null) {
                    Column(verticalArrangement = Arrangement.spacedBy(7.dp)) {
                        SpecRow(stringResource(R.string.spec_location), "${activeLocation.name.uppercase()} · ${activeLocation.code}")
                        SpecRow(stringResource(R.string.spec_server), "${activeLocation.host}:${activeLocation.port}")
                        SpecRow(
                            stringResource(R.string.spec_ping),
                            if (snap.selectedLocation == "recommended") "Anycast"
                            else CoreBridge.formatPing(activeLocation.latencyMs)
                        )
                        SpecRow(
                            stringResource(R.string.spec_protocol),
                            if (snap.protocol == "h3") "HTTP/3 MASQUE" else "HTTP/2 CONNECT"
                        )
                    }
                }

                BreakerSwitch(
                    isOn = isConnected,
                    isConnecting = isConnecting,
                    onToggle = onToggleTunnel
                )

                // Протокол: HTTP/2 первым — он по умолчанию и единственный
                // рабочий вариант (HTTP/3/QUIC во многих сетях нестабилен).
                SegmentedBreaker(
                    options = listOf("HTTP/2 CONNECT", "HTTP/3 MASQUE"),
                    selectedIndex = if (snap.protocol == "h3") 1 else 0,
                    onSelect = { idx -> onProtocolChange(if (idx == 1) "h3" else "h2") }
                )
            }
        }

        // 1b. Домашние сайты: адреса выбранной страны идут напрямую,
        // в обход туннеля (geoip в ядре; основной кейс — госуслуги и банки).
        item {
            Column(verticalArrangement = Arrangement.spacedBy(10.dp)) {
                SectionLabel(stringResource(R.string.home_sites))
                val countryName = CoreBridge.homeCountryName(snap.homeCountry)
                Column(
                    modifier = Modifier
                        .fillMaxWidth()
                        .background(Paper)
                        .border(BorderStroke(2.dp, Ink), RectangleShape)
                        .padding(13.dp),
                    verticalArrangement = Arrangement.spacedBy(9.dp)
                ) {
                    Row(verticalAlignment = Alignment.CenterVertically) {
                        if (snap.bypass) {
                            Box(
                                modifier = Modifier
                                    .size(9.dp)
                                    .background(Signal)
                            )
                            Spacer(Modifier.width(8.dp))
                        }
                        Text(
                            text = stringResource(R.string.bypass_tunnel),
                            color = if (snap.bypass) Ink else InkSoft,
                            fontSize = 13.sp,
                            fontFamily = PriborFonts.mono(FontWeight.Bold),
                            fontWeight = FontWeight.Bold,
                            modifier = Modifier.weight(1f)
                        )
                        MiniBreakerToggle(
                            checked = snap.bypass,
                            onClick = { onBypassChange(!snap.bypass) }
                        )
                    }
                    Box(
                        modifier = Modifier
                            .fillMaxWidth()
                            .clickable { onOpenHomePicker() }
                    ) {
                        SpecRow(
                            stringResource(R.string.your_country),
                            "${snap.homeCountry} · ${countryName.uppercase()} ›"
                        )
                    }
                    Text(
                        text = stringResource(
                            if (snap.bypass) R.string.bypass_note_on else R.string.bypass_note_off
                        ),
                        color = InkSoft,
                        fontSize = 11.sp,
                        fontFamily = PriborFonts.mono(FontWeight.Normal)
                    )
                }
            }
        }

        // 2. Аккаунты — компактные паспорта с барабаном квоты. На главной
        // только включённые (в ротации) плюс текущий активный: аккаунт,
        // выключенный на вкладке «Аккаунты», не должен на главной
        // числиться «готовым» (просьба пользователя, 2026-11-16).
        // Индексы остаются пуловыми — клик выбирает по настоящему индексу.
        val shownAccounts = snap.accounts.withIndex()
            .filter { it.value.enabled || it.index == snap.activeAccount }
        if (shownAccounts.isNotEmpty()) {
            item {
                Column(verticalArrangement = Arrangement.spacedBy(10.dp)) {
                    SectionLabel(stringResource(R.string.home_accounts, shownAccounts.size))
                    shownAccounts.forEach { (idx, acc) ->
                        val isActive = idx == snap.activeAccount
                        val pct = ((acc.usedBytes.toDouble() / acc.limitBytes.coerceAtLeast(1L).toDouble()) * 100.0)
                            .roundToInt()
                            .coerceIn(0, 100)

                        Column(
                            modifier = Modifier
                                .fillMaxWidth()
                                .background(Paper)
                                .border(
                                    BorderStroke(if (isActive) 2.dp else 1.dp, Ink),
                                    RectangleShape
                                )
                                .clickable { onSelectAccount(idx) }
                                .padding(13.dp),
                            verticalArrangement = Arrangement.spacedBy(9.dp)
                        ) {
                            AccountPlateHeader(acc = acc, isActive = isActive)
                            QuotaDrum(
                                fraction = acc.usedBytes.toDouble() / acc.limitBytes.coerceAtLeast(1L).toDouble(),
                                alarm = !acc.unlimited && pct >= snap.thresholdPct
                            )
                            Row(
                                modifier = Modifier.fillMaxWidth(),
                                horizontalArrangement = Arrangement.SpaceBetween
                            ) {
                                Text(
                                    text = if (acc.unlimited) stringResource(R.string.quota_unlimited)
                                    else stringResource(R.string.quota_used_line, CoreBridge.formatBytes(acc.usedBytes), CoreBridge.formatBytes(acc.limitBytes)),
                                    color = InkSoft,
                                    fontSize = 11.sp,
                                    fontFamily = PriborFonts.mono(FontWeight.Normal)
                                )
                                Text(
                                    text = if (acc.unlimited) "" else stringResource(R.string.quota_threshold_line, pct, snap.thresholdPct),
                                    color = InkSoft,
                                    fontSize = 11.sp,
                                    fontFamily = PriborFonts.mono(FontWeight.Normal)
                                )
                            }
                        }
                    }
                }
            }
        }

        // 3. Быстрые локации — строки табло.
        item {
            Column(verticalArrangement = Arrangement.spacedBy(10.dp)) {
                SectionLabel(stringResource(R.string.home_locations_fast))
                Column(
                    modifier = Modifier
                        .fillMaxWidth()
                        .border(BorderStroke(1.dp, Ink), RectangleShape)
                ) {
                    LocationRow(
                        code = stringResource(R.string.auto_row_code),
                        title = stringResource(R.string.fastest_title),
                        subtitle = stringResource(R.string.fastest_subtitle),
                        pingText = "",
                        selected = snap.selectedLocation == "fastest",
                        onClick = { onSelectLocation("fastest") }
                    )
                    snap.locations.take(5).forEach { loc ->
                        Box(
                            modifier = Modifier
                                .fillMaxWidth()
                                .height(1.dp)
                                .background(Ink)
                        )
                        val selected = snap.selectedLocation.equals(loc.code, ignoreCase = true)
                        LocationRow(
                            code = loc.code,
                            title = loc.name,
                            subtitle = loc.city,
                            pingText = CoreBridge.formatPing(loc.latencyMs),
                            selected = selected,
                            onClick = { onSelectLocation(loc.code) }
                        )
                    }
                }
            }
        }

    }
}

// ============================================================================
// TAB 2: АККАУНТЫ
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
    // Удаление аккаунта — только после подтверждения: действие
    // необратимо, а кнопка стоит в одном ряду с остальными.
    var pendingDeleteIdx by remember { mutableStateOf<Int?>(null) }
    if (pendingDeleteIdx != null) {
        val delIdx = pendingDeleteIdx!!
        val delEmail = snap.accounts.getOrNull(delIdx)?.email ?: ""
        androidx.compose.ui.window.Dialog(onDismissRequest = { pendingDeleteIdx = null }) {
            Column(
                modifier = Modifier
                    .fillMaxWidth()
                    .background(Paper)
                    .border(BorderStroke(2.dp, Ink), RectangleShape)
                    .padding(18.dp),
                verticalArrangement = Arrangement.spacedBy(12.dp)
            ) {
                Text(
                    text = stringResource(R.string.delete_confirm_title),
                    color = Ink,
                    fontSize = 20.sp,
                    fontFamily = PriborFonts.display
                )
                Text(
                    text = stringResource(R.string.delete_confirm_text, delEmail),
                    color = InkSoft,
                    fontSize = 13.sp,
                    fontFamily = PriborFonts.mono(FontWeight.Normal),
                    lineHeight = 18.sp
                )
                Row(horizontalArrangement = Arrangement.spacedBy(10.dp)) {
                    Box(
                        modifier = Modifier
                            .border(BorderStroke(1.dp, Ink), RectangleShape)
                            .clickable { pendingDeleteIdx = null }
                            .padding(horizontal = 14.dp, vertical = 9.dp)
                    ) {
                        Text(
                            text = stringResource(R.string.btn_cancel),
                            color = Ink,
                            fontSize = 12.sp,
                            fontFamily = PriborFonts.mono(FontWeight.SemiBold),
                            fontWeight = FontWeight.SemiBold
                        )
                    }
                    Box(
                        modifier = Modifier
                            .background(Alarm)
                            .clickable {
                                onRemoveAccount(delIdx)
                                pendingDeleteIdx = null
                            }
                            .padding(horizontal = 14.dp, vertical = 9.dp)
                    ) {
                        Text(
                            text = stringResource(R.string.btn_delete),
                            color = Paper,
                            fontSize = 12.sp,
                            fontFamily = PriborFonts.mono(FontWeight.SemiBold),
                            fontWeight = FontWeight.SemiBold
                        )
                    }
                }
            }
        }
    }

    LazyColumn(
        modifier = Modifier
            .fillMaxSize()
            .padding(horizontal = 20.dp),
        contentPadding = PaddingValues(vertical = 20.dp),
        verticalArrangement = Arrangement.spacedBy(16.dp)
    ) {
        // Шапка вкладки.
        item {
            Column(verticalArrangement = Arrangement.spacedBy(10.dp)) {
                Row(
                    modifier = Modifier.fillMaxWidth(),
                    horizontalArrangement = Arrangement.SpaceBetween,
                    verticalAlignment = Alignment.CenterVertically
                ) {
                    Text(
                        text = stringResource(R.string.accounts_title),
                        color = Ink,
                        fontSize = 30.sp,
                        fontFamily = PriborFonts.display
                    )
                    SmallPlateButton(
                        text = if (showAddForm) stringResource(R.string.btn_close) else stringResource(R.string.btn_add),
                        onClick = onToggleAddForm
                    )
                }
                Text(
                    text = stringResource(R.string.accounts_subline, snap.accounts.size),
                    color = InkSoft,
                    fontSize = 11.sp,
                    fontFamily = PriborFonts.mono(FontWeight.Normal),
                    lineHeight = 15.sp
                )
            }
        }

        // Баннер обратной связи.
        if (!authMessage.isNullOrBlank()) {
            item {
                Row(
                    modifier = Modifier
                        .fillMaxWidth()
                        .border(
                            BorderStroke(2.dp, if (authIsError) Alarm else Ink),
                            RectangleShape
                        )
                        .background(Paper)
                ) {
                    Box(
                        modifier = Modifier
                            .width(7.dp)
                            .height(54.dp)
                            .background(if (authIsError) Alarm else Signal)
                    )
                    Text(
                        text = authMessage,
                        color = Ink,
                        fontSize = 13.sp,
                        fontFamily = PriborFonts.mono(FontWeight.Normal),
                        lineHeight = 18.sp,
                        modifier = Modifier.padding(13.dp)
                    )
                }
            }
        }

        // Бланк добавления аккаунта.
        if (showAddForm || snap.accounts.isEmpty() || authStepState != "idle") {
            item {
                Column(
                    modifier = Modifier
                        .fillMaxWidth()
                        .background(Paper)
                        .border(BorderStroke(2.dp, Ink), RectangleShape)
                        .padding(16.dp),
                    verticalArrangement = Arrangement.spacedBy(14.dp)
                ) {
                    Text(
                        text = stringResource(R.string.add_form_title),
                        color = Ink,
                        fontSize = 13.sp,
                        fontFamily = PriborFonts.mono(FontWeight.SemiBold),
                        fontWeight = FontWeight.SemiBold,
                        lineHeight = 17.sp
                    )

                    if (authStepState == "verify_email" || authStepState == "verify_totp") {
                        Text(
                            text = if (authStepState == "verify_totp")
                                stringResource(R.string.verify_totp_prompt)
                            else
                                stringResource(R.string.verify_email_prompt),
                            color = Ink,
                            fontSize = 13.sp,
                            fontFamily = PriborFonts.mono(FontWeight.Normal),
                            lineHeight = 18.sp
                        )

                        CodeCells(
                            label = stringResource(R.string.code_label_6),
                            value = verifyCodeInput,
                            length = 6,
                            digitsOnly = true,
                            onValueChange = onVerifyCodeChange
                        )

                        Row(
                            modifier = Modifier.fillMaxWidth(),
                            horizontalArrangement = Arrangement.spacedBy(10.dp)
                        ) {
                            GhostButton(
                                text = stringResource(R.string.btn_back),
                                onClick = onCancelVerifyStep,
                                modifier = Modifier.weight(1f)
                            )
                            PlateButton(
                                text = stringResource(R.string.btn_confirm_code),
                                onClick = onSubmitVerifyCode,
                                enabled = !isSigningIn,
                                busy = isSigningIn,
                                modifier = Modifier.weight(2f)
                            )
                        }
                    } else {
                        SegmentedBreaker(
                            options = listOf(
                                stringResource(R.string.method_password),
                                stringResource(R.string.method_browser),
                                stringResource(R.string.method_token)
                            ),
                            selectedIndex = authMethod,
                            onSelect = onAuthMethodChange
                        )

                        when (authMethod) {
                            0 -> {
                                FormField(
                                    label = stringResource(R.string.field_email),
                                    value = emailInput,
                                    onValueChange = onEmailChange,
                                    keyboardOptions = KeyboardOptions(
                                        keyboardType = KeyboardType.Email,
                                        imeAction = ImeAction.Next
                                    )
                                )

                                FormField(
                                    label = stringResource(R.string.field_password),
                                    value = passwordInput,
                                    onValueChange = onPasswordChange,
                                    keyboardOptions = KeyboardOptions(
                                        keyboardType = KeyboardType.Password,
                                        imeAction = ImeAction.Done
                                    ),
                                    visualTransformation = if (showPassword) {
                                        VisualTransformation.None
                                    } else {
                                        PasswordVisualTransformation()
                                    },
                                    trailingText = if (showPassword) stringResource(R.string.btn_hide) else stringResource(R.string.btn_show),
                                    onTrailingClick = onToggleShowPassword
                                )

                                if (authStepState == "unblock_code") {
                                    CodeCells(
                                        label = stringResource(R.string.unblock_label),
                                        value = unblockCodeInput,
                                        length = 8,
                                        digitsOnly = false,
                                        onValueChange = onUnblockCodeChange
                                    )
                                }

                                PlateButton(
                                    text = if (isSigningIn) stringResource(R.string.btn_signing_in) else stringResource(R.string.btn_save_proxypass),
                                    onClick = onSubmitPasswordLogin,
                                    enabled = !isSigningIn && emailInput.isNotBlank() && passwordInput.isNotEmpty(),
                                    busy = isSigningIn,
                                    modifier = Modifier.fillMaxWidth()
                                )
                            }

                            1 -> {
                                Text(
                                    text = stringResource(R.string.webview_hint),
                                    color = InkSoft,
                                    fontSize = 13.sp,
                                    fontFamily = PriborFonts.mono(FontWeight.Normal),
                                    lineHeight = 18.sp
                                )

                                PlateButton(
                                    text = stringResource(R.string.btn_open_webview),
                                    onClick = onOpenBrowserSignIn,
                                    enabled = !isSigningIn,
                                    modifier = Modifier.fillMaxWidth()
                                )
                            }

                            2 -> {
                                FormField(
                                    label = stringResource(R.string.field_email_optional),
                                    value = emailInput,
                                    onValueChange = onEmailChange,
                                    keyboardOptions = KeyboardOptions(
                                        keyboardType = KeyboardType.Email,
                                        imeAction = ImeAction.Next
                                    )
                                )

                                FormField(
                                    label = stringResource(R.string.field_session_token),
                                    value = sessionTokenInput,
                                    onValueChange = onSessionTokenChange,
                                    keyboardOptions = KeyboardOptions(imeAction = ImeAction.Done)
                                )

                                PlateButton(
                                    text = stringResource(R.string.btn_save_proxypass),
                                    onClick = onSubmitSessionToken,
                                    enabled = !isSigningIn && sessionTokenInput.trim().length >= 32,
                                    busy = isSigningIn,
                                    modifier = Modifier.fillMaxWidth()
                                )
                            }
                        }
                    }
                }
            }
        }

        // Паспорта аккаунтов.
        itemsIndexed(snap.accounts) { idx, acc ->
            val isActive = idx == snap.activeAccount
            val pct = ((acc.usedBytes.toDouble() / acc.limitBytes.coerceAtLeast(1L).toDouble()) * 100.0)
                .roundToInt()
                .coerceIn(0, 100)

            Column(
                modifier = Modifier
                    .fillMaxWidth()
                    .background(Paper)
                    .border(
                        BorderStroke(if (isActive) 2.dp else 1.dp, Ink),
                        RectangleShape
                    )
                    .padding(15.dp),
                verticalArrangement = Arrangement.spacedBy(11.dp)
            ) {
                Column(verticalArrangement = Arrangement.spacedBy(5.dp)) {
                    Box(modifier = Modifier.clickable { onSelectAccount(idx) }) {
                        AccountPlateHeader(acc = acc, isActive = isActive)
                    }
                    Text(
                        text = when {
                            acc.status == "reauth_required" -> stringResource(R.string.acc_profile_out)
                            isActive -> stringResource(R.string.acc_profile_active)
                            !acc.enabled -> stringResource(R.string.acc_profile_off)
                            acc.exhausted -> stringResource(R.string.acc_profile_reserve)
                            else -> stringResource(R.string.acc_profile_queue)
                        },
                        color = InkSoft,
                        fontSize = 11.sp,
                        fontFamily = PriborFonts.mono(FontWeight.Normal),
                        letterSpacing = 1.sp
                    )
                }

                QuotaDrum(
                    fraction = acc.usedBytes.toDouble() / acc.limitBytes.coerceAtLeast(1L).toDouble(),
                    alarm = !acc.unlimited && pct >= snap.thresholdPct
                )
                Text(
                    text = if (acc.unlimited) stringResource(R.string.quota_unlimited)
                    else "${CoreBridge.formatBytes(acc.usedBytes)} / ${CoreBridge.formatBytes(acc.limitBytes)} ($pct%)",
                    color = InkSoft,
                    fontSize = 12.sp,
                    fontFamily = PriborFonts.mono(FontWeight.Normal)
                )

                Box(
                    modifier = Modifier
                        .fillMaxWidth()
                        .height(1.dp)
                        .background(Ink)
                )

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
                                .border(BorderStroke(1.dp, if (idx > 0) Ink else InkSoft), RectangleShape)
                                .clickable(enabled = idx > 0) { onMoveAccount(idx, -1) }
                                .padding(horizontal = 11.dp, vertical = 6.dp)
                        ) {
                            Text(
                                text = "↑",
                                color = if (idx > 0) Ink else InkSoft,
                                fontSize = 14.sp
                            )
                        }
                        Box(
                            modifier = Modifier
                                .border(
                                    BorderStroke(1.dp, if (idx < snap.accounts.size - 1) Ink else InkSoft),
                                    RectangleShape
                                )
                                .clickable(enabled = idx < snap.accounts.size - 1) { onMoveAccount(idx, 1) }
                                .padding(horizontal = 11.dp, vertical = 6.dp)
                        ) {
                            Text(
                                text = "↓",
                                color = if (idx < snap.accounts.size - 1) Ink else InkSoft,
                                fontSize = 14.sp
                            )
                        }
                        if (!isActive && acc.status != "reauth_required") {
                            Box(
                                modifier = Modifier
                                    .background(Ink)
                                    .border(BorderStroke(1.dp, Ink), RectangleShape)
                                    .clickable { onSelectAccount(idx) }
                                    .padding(horizontal = 11.dp, vertical = 6.dp)
                            ) {
                                Text(
                                    text = stringResource(R.string.btn_to_work),
                                    color = Paper,
                                    fontSize = 11.sp,
                                    fontFamily = PriborFonts.mono(FontWeight.SemiBold),
                                    fontWeight = FontWeight.SemiBold
                                )
                            }
                        }
                        if (acc.status != "reauth_required") {
                            Box(
                                modifier = Modifier
                                    .background(if (acc.enabled) Ink else Paper)
                                    .border(BorderStroke(1.dp, Ink), RectangleShape)
                                    .clickable { onToggleAccountEnabled(idx) }
                                    .padding(horizontal = 11.dp, vertical = 6.dp)
                            ) {
                                Text(
                                    text = if (acc.enabled) stringResource(R.string.btn_in_rotation) else stringResource(R.string.btn_acc_off),
                                    color = if (acc.enabled) Paper else InkSoft,
                                    fontSize = 11.sp,
                                    fontFamily = PriborFonts.mono(FontWeight.SemiBold),
                                    fontWeight = FontWeight.SemiBold
                                )
                            }
                        }
                    }

                    Text(
                        text = stringResource(R.string.btn_delete),
                        color = Alarm,
                        fontSize = 12.sp,
                        fontFamily = PriborFonts.mono(FontWeight.SemiBold),
                        fontWeight = FontWeight.SemiBold,
                        modifier = Modifier
                            .clickable { pendingDeleteIdx = idx }
                            .padding(horizontal = 6.dp, vertical = 6.dp)
                    )
                }
            }
        }

        // Порог авто-переключения.
        item {
            Column(
                modifier = Modifier
                    .fillMaxWidth()
                    .background(Paper)
                    .border(BorderStroke(1.dp, Ink), RectangleShape)
                    .padding(15.dp),
                verticalArrangement = Arrangement.spacedBy(9.dp)
            ) {
                Row(
                    modifier = Modifier.fillMaxWidth(),
                    horizontalArrangement = Arrangement.SpaceBetween,
                    verticalAlignment = Alignment.CenterVertically
                ) {
                    Text(
                        text = stringResource(R.string.threshold_title),
                        color = Ink,
                        fontSize = 12.sp,
                        fontFamily = PriborFonts.mono(FontWeight.SemiBold),
                        fontWeight = FontWeight.SemiBold,
                        modifier = Modifier.weight(1f)
                    )
                    Text(
                        text = stringResource(R.string.threshold_value, snap.thresholdPct),
                        color = Ink,
                        fontSize = 13.sp,
                        fontFamily = PriborFonts.mono(FontWeight.SemiBold),
                        fontWeight = FontWeight.SemiBold
                    )
                }

                Slider(
                    value = snap.thresholdPct.toFloat(),
                    onValueChange = { onThresholdChange(it.roundToInt()) },
                    valueRange = 50f..100f,
                    steps = 9,
                    colors = SliderDefaults.colors(
                        thumbColor = Ink,
                        activeTrackColor = Ink,
                        inactiveTrackColor = PaperDeep
                    )
                )

                Text(
                    text = stringResource(R.string.threshold_hint, snap.thresholdPct, snap.accounts.getOrNull(snap.activeAccount)?.let { " (${CoreBridge.formatBytes(it.limitBytes)})" } ?: ""),
                    color = InkSoft,
                    fontSize = 12.sp,
                    fontFamily = PriborFonts.mono(FontWeight.Normal),
                    lineHeight = 16.sp
                )
            }
        }
    }
}

// ============================================================================
// TAB 3: СЕРВЕРЫ (ЛОКАЦИИ)
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
        Row(
            modifier = Modifier.fillMaxWidth(),
            horizontalArrangement = Arrangement.SpaceBetween,
            verticalAlignment = Alignment.CenterVertically
        ) {
            Column(
                modifier = Modifier.weight(1f),
                verticalArrangement = Arrangement.spacedBy(5.dp)
            ) {
                Text(
                    text = stringResource(R.string.servers_title),
                    color = Ink,
                    fontSize = 30.sp,
                    fontFamily = PriborFonts.display
                )
                Text(
                    text = stringResource(R.string.servers_subline, snap.locations.size),
                    color = InkSoft,
                    fontSize = 11.sp,
                    fontFamily = PriborFonts.mono(FontWeight.Normal),
                    lineHeight = 15.sp
                )
            }

            Spacer(modifier = Modifier.width(10.dp))

            SmallPlateButton(
                text = if (isPinging) stringResource(R.string.btn_pinging) else stringResource(R.string.btn_ping),
                onClick = onPingAll,
                enabled = !isPinging,
                busy = isPinging
            )
        }

        FormField(
            label = stringResource(R.string.search_label),
            value = searchQuery,
            onValueChange = onSearchChange,
            placeholder = stringResource(R.string.search_hint),
            keyboardOptions = KeyboardOptions(imeAction = ImeAction.Search)
        )

        // Режимы: рекомендуемый / быстрейший — две пластины табло.
        Row(
            modifier = Modifier.fillMaxWidth(),
            horizontalArrangement = Arrangement.spacedBy(8.dp)
        ) {
            val isRecommended = snap.selectedLocation == "recommended"
            val isFastest = snap.selectedLocation == "fastest"

            ModePlate(
                title = stringResource(R.string.mode_recommended),
                subtitle = "Anycast (REC)",
                selected = isRecommended,
                onClick = { onSelectLocation("recommended") },
                modifier = Modifier.weight(1f)
            )
            ModePlate(
                title = stringResource(R.string.mode_fastest),
                subtitle = stringResource(R.string.mode_fastest_sub),
                selected = isFastest,
                onClick = { onSelectLocation("fastest") },
                modifier = Modifier.weight(1f)
            )
        }

        // Табло локаций.
        LazyColumn(
            modifier = Modifier
                .weight(1f)
                .fillMaxWidth()
                .border(BorderStroke(1.dp, Ink), RectangleShape)
                .background(Paper)
        ) {
            itemsIndexed(filteredLocations, key = { _, it -> it.code }) { idx, loc ->
                val isSelected = snap.selectedLocation.equals(loc.code, ignoreCase = true)
                val pingColor = when {
                    loc.latencyMs <= 0 -> InkSoft
                    loc.latencyMs >= 140 -> Signal
                    else -> Ink
                }

                LocationRow(
                    code = loc.code,
                    title = loc.name,
                    subtitle = "${loc.city} · ${loc.host}",
                    pingText = CoreBridge.formatPing(loc.latencyMs),
                    pingColor = pingColor,
                    selected = isSelected,
                    onClick = { onSelectLocation(loc.code) }
                )

                if (idx < filteredLocations.size - 1) {
                    Box(
                        modifier = Modifier
                            .fillMaxWidth()
                            .height(1.dp)
                            .background(Ink)
                    )
                }
            }
        }
    }
}

@Composable
private fun ModePlate(
    title: String,
    subtitle: String,
    selected: Boolean,
    onClick: () -> Unit,
    modifier: Modifier = Modifier
) {
    Row(
        modifier = modifier
            .background(if (selected) PaperDeep else Paper)
            .border(BorderStroke(if (selected) 2.dp else 1.dp, Ink), RectangleShape)
            .clickable(onClick = onClick),
        verticalAlignment = Alignment.CenterVertically
    ) {
        Box(
            modifier = Modifier
                .width(6.dp)
                .height(58.dp)
                .background(if (selected) Signal else Color.Transparent)
        )
        Column(
            modifier = Modifier.padding(horizontal = 12.dp, vertical = 10.dp),
            verticalArrangement = Arrangement.spacedBy(3.dp)
        ) {
            Text(
                text = title,
                color = Ink,
                fontSize = 13.sp,
                fontFamily = PriborFonts.mono(FontWeight.SemiBold),
                fontWeight = FontWeight.SemiBold,
                maxLines = 1
            )
            Text(
                text = subtitle,
                color = InkSoft,
                fontSize = 11.sp,
                fontFamily = PriborFonts.mono(FontWeight.Normal),
                maxLines = 1
            )
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
    var webViewRef by remember { mutableStateOf<WebView?>(null) }

    DisposableEffect(Unit) {
        onDispose {
            webViewRef?.let { wv ->
                try {
                    wv.stopLoading()
                    wv.destroy()
                } catch (_: Throwable) {}
            }
            webViewRef = null
        }
    }

    Dialog(
        onDismissRequest = onDismiss,
        properties = DialogProperties(usePlatformDefaultWidth = false)
    ) {
        Column(
            modifier = Modifier
                .fillMaxSize()
                .background(Paper)
                .statusBarsPadding()
                .navigationBarsPadding()
        ) {
            Row(
                modifier = Modifier
                    .fillMaxWidth()
                    .padding(horizontal = 16.dp, vertical = 12.dp),
                horizontalArrangement = Arrangement.SpaceBetween,
                verticalAlignment = Alignment.CenterVertically
            ) {
                Column {
                    Text(
                        text = stringResource(R.string.webview_title),
                        color = Ink,
                        fontSize = 14.sp,
                        fontFamily = PriborFonts.mono(FontWeight.SemiBold),
                        fontWeight = FontWeight.SemiBold
                    )
                    Text(
                        text = "accounts.firefox.com",
                        color = InkSoft,
                        fontSize = 12.sp,
                        fontFamily = PriborFonts.mono(FontWeight.Normal)
                    )
                }

                Text(
                    text = stringResource(R.string.btn_close),
                    color = Ink,
                    fontSize = 13.sp,
                    fontFamily = PriborFonts.mono(FontWeight.SemiBold),
                    fontWeight = FontWeight.SemiBold,
                    modifier = Modifier
                        .clickable(onClick = onDismiss)
                        .padding(6.dp)
                )
            }

            Box(
                modifier = Modifier
                    .fillMaxWidth()
                    .height(2.dp)
                    .background(Ink)
            )

            AndroidView(
                modifier = Modifier
                    .weight(1f)
                    .fillMaxWidth(),
                factory = { ctx ->
                    WebView(ctx).apply {
                        webViewRef = this
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
                                            // Token captured — the login
                                            // WebView's cookies and storage
                                            // are no longer needed; wipe
                                            // them so nothing from the
                                            // session lingers in the shared
                                            // CookieManager.
                                            CookieManager.getInstance().removeAllCookies(null)
                                            CookieManager.getInstance().flush()
                                            android.webkit.WebStorage.getInstance().deleteAllData()
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
                                            if (data.sessionToken) {
                                                if (data.verified === false) {
                                                    // Session not confirmed yet: FxA will
                                                    // ask for the email/TOTP code on this very
                                                    // page and then send fxaccounts:verified.
                                                    // Capturing now would only buy an
                                                    // errno 138 from the token exchange.
                                                    window.__mozVpnPendingLogin = data;
                                                } else if (window.MozillaVpnBridge) {
                                                    window.MozillaVpnBridge.onFxaLogin(data.email || '', data.sessionToken);
                                                }
                                            }
                                        } else if (cmd === 'fxaccounts:verified') {
                                            var pending = window.__mozVpnPendingLogin || {};
                                            var token = data.sessionToken || pending.sessionToken;
                                            var email = data.email || pending.email || '';
                                            window.__mozVpnPendingLogin = null;
                                            if (token && window.MozillaVpnBridge) {
                                                window.MozillaVpnBridge.onFxaLogin(email, token);
                                            }
                                        }
                                    } catch (err) {}
                                }, true);
                            })();
                        """.trimIndent()

                        webViewClient = object : WebViewClient() {
                            // The WebChannel hook (and with it the
                            // MozillaVpnBridge) exists only for the FxA
                            // page itself: inject only there, and keep
                            // in-WebView navigation inside Mozilla
                            // domains (hardening, 2026-11-16).
                            private fun isFxaPage(url: String?): Boolean =
                                url != null &&
                                    android.net.Uri.parse(url).host == "accounts.firefox.com"

                            private fun isMozillaHost(host: String?): Boolean {
                                if (host == null) return true // about:blank, data:
                                return host == "firefox.com" || host == "mozilla.com" ||
                                    host == "mozilla.org" ||
                                    host.endsWith(".firefox.com") ||
                                    host.endsWith(".mozilla.com") ||
                                    host.endsWith(".mozilla.org")
                            }

                            override fun onPageStarted(view: WebView?, url: String?, favicon: android.graphics.Bitmap?) {
                                super.onPageStarted(view, url, favicon)
                                if (isFxaPage(url)) {
                                    view?.evaluateJavascript(injectScript, null)
                                }
                            }

                            override fun onPageFinished(view: WebView?, url: String?) {
                                super.onPageFinished(view, url)
                                if (isFxaPage(url)) {
                                    view?.evaluateJavascript(injectScript, null)
                                }
                            }

                            override fun shouldOverrideUrlLoading(
                                view: WebView?,
                                request: WebResourceRequest?
                            ): Boolean = !isMozillaHost(request?.url?.host)
                        }

                        loadUrl(fxaUrl)
                    }
                }
            )
        }
    }
}
