package com.universalclipboard.navigation

import androidx.compose.foundation.layout.padding
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.automirrored.filled.ArrowBack
import androidx.compose.material.icons.filled.Home
import androidx.compose.material.icons.filled.Settings
import androidx.compose.material3.ExperimentalMaterial3Api
import androidx.compose.material3.Icon
import androidx.compose.material3.IconButton
import androidx.compose.material3.NavigationBar
import androidx.compose.material3.NavigationBarItem
import androidx.compose.material3.Scaffold
import androidx.compose.material3.Text
import androidx.compose.material3.TopAppBar
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.painter.Painter
import androidx.compose.ui.graphics.vector.rememberVectorPainter
import androidx.compose.ui.res.painterResource
import androidx.compose.ui.res.stringResource
import androidx.navigation.NavGraph.Companion.findStartDestination
import androidx.navigation.NavType
import androidx.navigation.compose.NavHost
import androidx.navigation.compose.composable
import androidx.navigation.compose.currentBackStackEntryAsState
import androidx.navigation.compose.rememberNavController
import androidx.navigation.navArgument
import com.universalclipboard.R
import com.universalclipboard.ui.devices.DevicesScreen
import com.universalclipboard.ui.history.HistoryScreen
import com.universalclipboard.ui.home.HomeScreen
import com.universalclipboard.ui.pairing.PairingScreen
import com.universalclipboard.ui.settings.SettingsScreen
import java.net.URLEncoder

object Routes {
    const val HOME = "home"
    const val DEVICES = "devices"
    const val HISTORY = "history"
    const val SETTINGS = "settings"
    const val PAIR = "pair?link={link}"
    fun pair(link: String? = null) = if (link == null) "pair" else "pair?link=${URLEncoder.encode(link, "UTF-8")}"
}

private data class Tab(val route: String, val label: Int, val icon: @Composable () -> Painter)

@OptIn(ExperimentalMaterial3Api::class)
@Composable
fun AppNavigation(pairingLink: String?, onPairingLinkConsumed: () -> Unit) {
    val nav = rememberNavController()
    val tabs = listOf(
        Tab(Routes.HOME, R.string.tab_home) { rememberVectorPainter(Icons.Default.Home) },
        Tab(Routes.DEVICES, R.string.tab_devices) { painterResource(R.drawable.ic_computer) },
        Tab(Routes.HISTORY, R.string.tab_history) { painterResource(R.drawable.ic_history) },
        Tab(Routes.SETTINGS, R.string.tab_settings) { rememberVectorPainter(Icons.Default.Settings) },
    )
    val entry by nav.currentBackStackEntryAsState()
    val route = entry?.destination?.route
    val onPairScreen = route == Routes.PAIR

    LaunchedEffect(pairingLink) {
        if (pairingLink != null) {
            nav.navigate(Routes.pair(pairingLink))
            onPairingLinkConsumed()
        }
    }

    Scaffold(
        topBar = {
            TopAppBar(
                title = { Text(stringResource(if (onPairScreen) R.string.title_pair else R.string.app_name)) },
                navigationIcon = {
                    if (onPairScreen) {
                        IconButton(onClick = { nav.popBackStack() }) { Icon(Icons.AutoMirrored.Filled.ArrowBack, stringResource(R.string.action_back)) }
                    }
                },
            )
        },
        bottomBar = {
            if (!onPairScreen) {
                NavigationBar {
                    tabs.forEach { tab ->
                        NavigationBarItem(
                            selected = route == tab.route,
                            onClick = {
                                nav.navigate(tab.route) {
                                    popUpTo(nav.graph.findStartDestination().id) { saveState = true }
                                    launchSingleTop = true
                                    restoreState = true
                                }
                            },
                            icon = { Icon(tab.icon(), null) },
                            label = { Text(stringResource(tab.label)) },
                        )
                    }
                }
            }
        },
    ) { padding ->
        val toPair = { nav.navigate(Routes.pair()) }
        NavHost(nav, startDestination = Routes.HOME, modifier = Modifier.padding(padding)) {
            composable(Routes.HOME) { HomeScreen(onPair = toPair) }
            composable(Routes.DEVICES) { DevicesScreen(onPair = toPair) }
            composable(Routes.HISTORY) { HistoryScreen() }
            composable(Routes.SETTINGS) { SettingsScreen() }
            composable(
                Routes.PAIR,
                arguments = listOf(navArgument("link") { type = NavType.StringType; nullable = true; defaultValue = null }),
            ) { backStack ->
                PairingScreen(
                    initialLink = backStack.arguments?.getString("link"),
                    onDone = {
                        nav.navigate(Routes.DEVICES) {
                            popUpTo(nav.graph.findStartDestination().id)
                            launchSingleTop = true
                        }
                    },
                )
            }
        }
    }
}
