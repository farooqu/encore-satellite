package com.encore.satellite

import android.annotation.SuppressLint
import android.text.format.DateFormat
import android.view.View
import android.webkit.*
import androidx.compose.foundation.background
import androidx.compose.foundation.gestures.awaitEachGesture
import androidx.compose.foundation.gestures.awaitFirstDown
import androidx.compose.foundation.layout.*
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import androidx.compose.foundation.text.BasicText
import androidx.compose.foundation.text.KeyboardOptions
import androidx.compose.foundation.text.TextAutoSize
import androidx.compose.foundation.selection.toggleable
import androidx.compose.material3.*
import androidx.compose.runtime.*
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.Brush
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.input.pointer.PointerEventPass
import androidx.compose.ui.input.pointer.changedToUp
import androidx.compose.ui.input.pointer.pointerInput
import androidx.compose.ui.platform.LocalConfiguration
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.platform.LocalDensity
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.semantics.CustomAccessibilityAction
import androidx.compose.ui.semantics.Role
import androidx.compose.ui.semantics.contentDescription
import androidx.compose.ui.semantics.customActions
import androidx.compose.ui.semantics.semantics
import androidx.compose.ui.text.TextStyle
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.input.KeyboardType
import androidx.compose.ui.text.style.TextAlign
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import androidx.compose.ui.viewinterop.AndroidView
import androidx.webkit.WebViewCompat
import androidx.webkit.WebViewFeature
import kotlinx.coroutines.delay
import org.json.JSONArray
import org.json.JSONObject
import java.net.URI
import java.util.Date
import java.util.Locale
import java.util.TimeZone
import kotlin.math.abs

internal const val MAX_DASHBOARDS = 5
internal data class DashboardPage(val path: String, val title: String)
internal data class DashboardSelection(val origin: String, val pages: List<DashboardPage>) {
    fun encode(): String = JSONObject().put("origin", origin).put("pages", JSONArray().apply {
        pages.forEach { put(JSONObject().put("url", it.path).put("title", it.title)) }
    }).toString()

    companion object {
        fun decode(value: String): DashboardSelection? = runCatching {
            val data = JSONObject(value)
            val origin = homeAssistantOrigin(data.getString("origin")) ?: return null
            DashboardSelection(origin, dashboardPages(data.getJSONArray("pages")).take(MAX_DASHBOARDS))
        }.getOrNull()
    }
}

internal fun homeAssistantOrigin(input: String): String? = runCatching {
    val uri = URI(input.trim())
    val scheme = uri.scheme?.lowercase(Locale.ROOT)
    val host = uri.host?.lowercase(Locale.ROOT) ?: return null
    if (scheme !in listOf("http", "https") || uri.userInfo != null || uri.rawQuery != null ||
        uri.rawFragment != null || uri.rawPath !in listOf("", "/") || uri.port != -1 && uri.port !in 1..65535) return null
    val defaultPort = if (scheme == "https") 443 else 80
    "$scheme://$host" + if (uri.port == -1 || uri.port == defaultPort) "" else ":${uri.port}"
}.getOrNull()

private fun dashboardPages(data: JSONArray): List<DashboardPage> =
    (0 until data.length()).mapNotNull { index ->
        val entry = data.optJSONObject(index) ?: return@mapNotNull null
        val path = entry.optString("url")
        val uri = runCatching { URI(path) }.getOrNull() ?: return@mapNotNull null
        if (!path.startsWith('/') || path.startsWith("//") || uri.rawPath != path ||
            uri.path.split('/').any { it == "." || it == ".." }) return@mapNotNull null
        DashboardPage(path, entry.optString("title").trim().take(160).ifBlank { path })
    }.distinctBy { it.path }

@Composable private fun DisplayClock() {
    val context = LocalContext.current
    val locale = LocalConfiguration.current.locales[0]
    val timeFormat = DateFormat.getTimeFormat(context)
    val dateFormat = DateFormat.getLongDateFormat(context)
    val dayFormat = java.text.SimpleDateFormat("EEEE", locale)
    val now by produceState(System.currentTimeMillis()) {
        while (true) {
            delay(60_000 - value % 60_000)
            value = System.currentTimeMillis()
        }
    }
    val date = Date(now)
    // The user can change time zones while the app is acting as Android Home.
    val zone = TimeZone.getDefault()
    timeFormat.timeZone = zone
    dateFormat.timeZone = zone
    dayFormat.timeZone = zone
    BoxWithConstraints(Modifier.fillMaxSize().testTag("display-clock").background(Brush.verticalGradient(
        listOf(Color(0xff101e29), Color(0xff0a121b)))),
        contentAlignment = Alignment.Center) {
        val landscape = maxWidth > maxHeight
        val maxTimeSize = with(LocalDensity.current) { (maxHeight * .48f).toSp().value.coerceAtLeast(12f).sp }
        // Keep the right-side control dock clear in landscape, even for a long localized time.
        val sidePadding = if (landscape) 80.dp else 16.dp
        Column(Modifier.padding(horizontal = sidePadding), horizontalAlignment = Alignment.CenterHorizontally) {
            Text(dayFormat.format(date).uppercase(locale),
                color = MaterialTheme.colorScheme.primary, fontSize = 12.sp, letterSpacing = 4.sp)
            BasicText(timeFormat.format(date), Modifier.fillMaxWidth().testTag("display-clock-time"),
                style = TextStyle(color = Color(0xfff1f7fa), fontWeight = FontWeight.Light, textAlign = TextAlign.Center),
                maxLines = 1, autoSize = TextAutoSize.StepBased(minFontSize = 12.sp, maxFontSize = maxTimeSize, stepSize = 1.sp))
            Text(dateFormat.format(date), color = Color(0xffb1c2d0), fontSize = 18.sp)
        }
    }
}

@Composable internal fun DashboardCarousel(pages: List<DashboardPage>, swipeEnabled: Boolean,
    content: @Composable (DashboardPage) -> Unit) {
    // Page 0 is the clock. Start on the first configured dashboard if there is one.
    var page by remember(pages) { mutableIntStateOf(if (pages.isEmpty()) 0 else 1) }
    val navigation = if (swipeEnabled && pages.isNotEmpty()) Modifier.testTag("dashboard-carousel")
        .semantics {
            customActions = buildList {
                if (page > 0) add(CustomAccessibilityAction("Previous display page") { page--; true })
                if (page < pages.size) add(CustomAccessibilityAction("Next display page") { page++; true })
            }
        }.pointerInput(pages) {
            val swipeDistance = 56.dp.toPx()
            awaitEachGesture {
                // Observe before the embedded WebView claims touch. Leave taps, vertical scrolling and
                // multi-touch to the content; cancel its drag only after a deliberate horizontal swipe.
                val down = awaitFirstDown(requireUnconsumed = false, pass = PointerEventPass.Initial)
                var claimed = false
                while (true) {
                    val change = awaitPointerEvent(PointerEventPass.Initial).changes.singleOrNull() ?: break
                    if (change.id != down.id) break
                    val (dx, dy) = change.position - down.position
                    val horizontal = abs(dx) > abs(dy) * 1.5f
                    val target = page + if (dx < 0f) 1 else -1
                    val canSwitch = horizontal && abs(dx) >= swipeDistance && target in 0..pages.size
                    if (!claimed) {
                        if (!change.pressed || (!horizontal && abs(dy) > viewConfiguration.touchSlop)) break
                        if (!canSwitch) continue
                        claimed = true
                    }
                    val released = change.changedToUp()
                    change.consume()
                    if (!change.pressed) {
                        if (released && canSwitch) page = target
                        break
                    }
                }
            }
        } else Modifier
    Box(Modifier.fillMaxSize().then(navigation)) {
        if (page == 0) DisplayClock() else content(pages[page - 1])
    }
}

@Composable internal fun DashboardPicker(origin: String, initial: List<DashboardPage>, auth: DashboardAuth?,
    close: () -> Unit, save: (List<DashboardPage>) -> Unit) {
    var selected by remember(origin) { mutableStateOf(initial) }
    var catalog by remember(origin) { mutableStateOf<List<DashboardPage>?>(null) }
    Column(Modifier.fillMaxSize().systemBarsPadding().imePadding(), horizontalAlignment = Alignment.CenterHorizontally) {
        Row(Modifier.widthIn(max = 640.dp).fillMaxWidth().padding(horizontal = 20.dp, vertical = 8.dp),
            verticalAlignment = Alignment.CenterVertically) {
            Text("Choose dashboards", Modifier.weight(1f), style = MaterialTheme.typography.titleLarge)
            TextButton(onClick = close) { Text("Cancel") }
            TextButton(onClick = {
                save(selected.map { page -> catalog?.find { it.path == page.path } ?: page })
            }, enabled = catalog != null) { Text("Save") }
        }
        val available = catalog
        if (available != null) {
            DashboardChoices(available, selected) { selected = it }
        } else if (auth != null && auth.lostOrigin == origin) {
            DashboardSignInNeeded(onPhone = if (supportsDashboardBridge()) ({ auth.start(origin) }) else null,
                onTablet = auth::manualSignIn)
        } else {
            Row(Modifier.fillMaxWidth().padding(horizontal = 16.dp), verticalAlignment = Alignment.CenterVertically) {
                Text("Sign in to Home Assistant to load dashboard views.", Modifier.weight(1f),
                    style = MaterialTheme.typography.bodySmall)
                if (auth != null && origin.startsWith("https://") && supportsDashboardBridge()) {
                    TextButton(onClick = { auth.start(origin) }) { Text("Sign in with phone") }
                }
            }
            DashboardWebView(origin, "$origin/", 0, auth, catalog = { catalog = it })
        }
    }
}

@Composable internal fun DashboardChoices(available: List<DashboardPage>, selected: List<DashboardPage>,
    onSelect: (List<DashboardPage>) -> Unit) {
    LazyColumn(Modifier.widthIn(max = 640.dp).fillMaxSize().testTag("dashboard-choices"), contentPadding = PaddingValues(20.dp)) {
        item {
            Text("${selected.size} of $MAX_DASHBOARDS selected", style = MaterialTheme.typography.titleMedium)
            Text(if (selected.size == MAX_DASHBOARDS) "Uncheck a page to add another."
                else "Pages appear in the order you select them. Leave all unchecked to show only the clock.",
                style = MaterialTheme.typography.bodyMedium)
            if (available.isEmpty() && selected.isEmpty()) Text("No dashboard views found for this account.")
        }
        items((available + selected).distinctBy { it.path }, key = { it.path }) { page ->
            val position = selected.indexOfFirst { it.path == page.path }
            val checked = position >= 0
            val enabled = checked || selected.size < MAX_DASHBOARDS
            Row(Modifier.fillMaxWidth().toggleable(checked, enabled = enabled, role = Role.Checkbox,
                onValueChange = { onSelect(if (it) selected + page else selected.filterNot { it.path == page.path }) })
                .heightIn(min = 52.dp), verticalAlignment = Alignment.CenterVertically) {
                Checkbox(checked, onCheckedChange = null, enabled = enabled, modifier = Modifier.padding(end = 16.dp))
                Text(page.title, Modifier.weight(1f), color = LocalContentColor.current.let { if (enabled) it else it.copy(alpha = .38f) })
                if (checked) Text("${position + 1}", color = MaterialTheme.colorScheme.primary)
            }
            HorizontalDivider()
        }
    }
}

@Composable internal fun DashboardSettings(origin: String, pages: List<DashboardPage>, choose: (String) -> Unit,
    update: (List<DashboardPage>) -> Unit) {
    var address by remember(origin) { mutableStateOf(origin) }
    val server = homeAssistantOrigin(address)
    Text("Dashboards", style = MaterialTheme.typography.titleMedium)
    if (pages.isEmpty()) Text("No dashboards selected. Encore shows the clock.")
    pages.forEachIndexed { index, page ->
        Row(verticalAlignment = Alignment.CenterVertically) {
            Text("${index + 1}. ${page.title}", Modifier.weight(1f), maxLines = 2, overflow = TextOverflow.Ellipsis)
            TextButton(onClick = { update(pages.filterIndexed { i, _ -> i != index }) }, modifier = Modifier.semantics {
                contentDescription = "Remove ${page.title}"
            }) { Text("Remove") }
        }
    }
    OutlinedTextField(address, { address = it }, label = { Text("Home Assistant address") }, singleLine = true,
        placeholder = { Text("https://home.example") }, keyboardOptions = KeyboardOptions(keyboardType = KeyboardType.Uri),
        modifier = Modifier.fillMaxWidth())
    Text("Enter the server address, without a dashboard path. Prefer HTTPS.", style = MaterialTheme.typography.bodySmall)
    TextButton(onClick = { server?.let(choose) }, enabled = server != null) { Text("Choose dashboards") }
    if (pages.isNotEmpty()) TextButton(onClick = { update(emptyList()) }) { Text("Show only clock") }
}

@SuppressLint("SetJavaScriptEnabled")
@Composable internal fun DashboardWebView(origin: String, url: String, reload: Int, auth: DashboardAuth?,
    hidden: Boolean = false, catalog: ((List<DashboardPage>) -> Unit)? = null) {
    val session = auth?.saved?.takeIf { it.origin() == origin && supportsDashboardBridge() }
    key(origin, session) {
        val context = LocalContext.current
        val picking = catalog != null
        val script = remember(picking) {
            val resource = if (picking) R.raw.dashboard_catalog else R.raw.dashboard_kiosk
            context.resources.openRawResource(resource).bufferedReader().use { it.readText() }
        }
        var webView by remember { mutableStateOf<WebView?>(null) }
        val latestCatalog by rememberUpdatedState(catalog)
        LaunchedEffect(webView, picking) {
            val view = webView ?: return@LaunchedEffect
            if (!picking) return@LaunchedEffect
            var found = false
            while (!found) {
                if (trustedNavigation(origin, view.url.orEmpty())) view.evaluateJavascript(script) { result ->
                    if (webView === view && trustedNavigation(origin, view.url.orEmpty())) {
                        runCatching { dashboardPages(JSONArray(result)) }.getOrNull()?.let { pages ->
                            found = true
                            latestCatalog?.invoke(pages)
                        }
                    }
                }
                delay(500)
            }
        }
        AndroidView(factory = { context -> WebView(context).apply {
            webView = this
            setBackgroundColor(android.graphics.Color.rgb(16, 29, 40))
            settings.javaScriptEnabled = true
            settings.domStorageEnabled = true
            settings.allowFileAccess = false
            settings.allowContentAccess = false
            settings.mixedContentMode = WebSettings.MIXED_CONTENT_NEVER_ALLOW
            settings.setSupportMultipleWindows(false)
            CookieManager.getInstance().setAcceptThirdPartyCookies(this, false)
            val documentStart = !picking && WebViewFeature.isFeatureSupported(WebViewFeature.DOCUMENT_START_SCRIPT)
            if (documentStart) WebViewCompat.addDocumentStartJavaScript(this, script, setOf(origin))
            webViewClient = object : WebViewClient() {
                override fun shouldOverrideUrlLoading(view: WebView, request: WebResourceRequest) =
                    !trustedNavigation(origin, request.url.toString())

                override fun onPageFinished(view: WebView, url: String) {
                    if (!picking && !documentStart && trustedNavigation(origin, url) && trustedNavigation(origin, view.url.orEmpty())) {
                        view.evaluateJavascript(script, null)
                    }
                }
            }
            if (session != null) auth.installBridge(this, session)
            // No addJavascriptInterface, downloads, external intents or WebChromeClient grants.
        } }, update = { view ->
            view.importantForAccessibility = if (hidden) View.IMPORTANT_FOR_ACCESSIBILITY_NO_HIDE_DESCENDANTS else View.IMPORTANT_FOR_ACCESSIBILITY_AUTO
            if (hidden) view.clearFocus()
            view.isFocusable = !hidden
            view.isFocusableInTouchMode = !hidden
            val desired = "$reload:$url"
            if (view.tag != desired) { view.tag = desired; view.loadUrl(dashboardLoadUrl(url, session != null)) }
        }, onRelease = { webView = null; it.destroy() }, modifier = Modifier.fillMaxSize().testTag("dashboard"))
    }
}
