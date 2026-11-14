import 'package:flutter/material.dart';

import '../app_settings.dart';
import '../core/android_core.dart';
import '../core/core_controller.dart';
import '../l10n/app_localizations.dart';
import 'accounts_page.dart';
import 'overview_page.dart';
import 'servers_page.dart';
import 'widgets.dart';

/// Minimalist 3-tab Android layout: Home (Quick Switch), Accounts, and Servers.
/// All announcements, logs, bypass settings, and bloat pages are removed.
class MobileShell extends StatefulWidget {
  const MobileShell({
    super.key,
    required this.core,
    required this.settings,
    required this.banner,
    this.noCore,
  });

  final CoreController core;
  final AppSettings settings;
  final Widget? banner;
  final Widget? noCore;

  @override
  State<MobileShell> createState() => _MobileShellState();
}

class _MobileShellState extends State<MobileShell> {
  int _tab = 0;

  @override
  void initState() {
    super.initState();
    AndroidCore.homeRequests.addListener(_openHome);
  }

  void _openHome() => setState(() => _tab = 0);

  @override
  void dispose() {
    AndroidCore.homeRequests.removeListener(_openHome);
    super.dispose();
  }

  static Widget _readable(Widget page) => Center(
    child: ConstrainedBox(
      constraints: const BoxConstraints(maxWidth: 720),
      child: page,
    ),
  );

  @override
  Widget build(BuildContext context) {
    final l = AppLocalizations.of(context);
    final core = widget.core;
    final settings = widget.settings;

    final accountsCount = core.accounts?.accounts.length ?? core.status?.totalAccounts ?? 0;

    final Widget activePage = switch (_tab) {
      0 => OverviewPage(
        core: core,
        settings: settings,
        onOpenServers: () => setState(() => _tab = 2),
        onOpenAccounts: () => setState(() => _tab = 1),
        phone: true,
      ),
      1 => OwnTopBar(
        child: _readable(AccountsPage(core: core, settings: settings)),
      ),
      _ => OwnTopBar(
        child: _readable(ServersPage(core: core, settings: settings)),
      ),
    };

    final page = widget.noCore ?? activePage;

    return PopScope(
      canPop: _tab == 0,
      onPopInvokedWithResult: (didPop, _) {
        if (!didPop) setState(() => _tab = 0);
      },
      child: Scaffold(
        body: SafeArea(
          top: _tab != 0 || widget.noCore != null || widget.banner != null,
          child: Column(
            children: [
              ?widget.banner,
              Expanded(child: page),
            ],
          ),
        ),
        bottomNavigationBar: _themed(
          context,
          home:
              _tab == 0 &&
              widget.noCore == null &&
              Theme.of(context).brightness == Brightness.dark,
          child: NavigationBar(
            selectedIndex: _tab,
            onDestinationSelected: (i) => setState(() => _tab = i),
            destinations: [
              NavigationDestination(
                icon: const Icon(Icons.power_settings_new_rounded),
                selectedIcon: const Icon(Icons.power_settings_new_rounded),
                label: l.navHome,
              ),
              NavigationDestination(
                icon: const Icon(Icons.people_outline_rounded),
                selectedIcon: const Icon(Icons.people_rounded),
                label: accountsCount > 0 ? '${l.navAccounts}  ($accountsCount)' : l.navAccounts,
              ),
              NavigationDestination(
                icon: const Icon(Icons.public_rounded),
                selectedIcon: const Icon(Icons.public_rounded),
                label: l.navServers,
              ),
            ],
          ),
        ),
      ),
    );
  }

  Widget _themed(
    BuildContext context, {
    required bool home,
    required Widget child,
  }) => home
      ? Theme(data: midnightTheme(Theme.of(context)), child: child)
      : child;
}
