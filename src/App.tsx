/**
 * @license
 * SPDX-License-Identifier: Apache-2.0
 */

import React, { useEffect, useState, useCallback } from 'react';
import { AppStateSnapshot } from './types';
import { MinimalAndroidPhone } from './components/MinimalAndroidPhone';
import { ProtocolAndAndroidWorkbench } from './components/ProtocolAndAndroidWorkbench';
import { formatBytes } from './utils/format';

export default function App() {
  const [state, setState] = useState<AppStateSnapshot | null>(null);
  const [loading, setLoading] = useState(true);
  const [mobileScreen, setMobileScreen] = useState<'home' | 'accounts' | 'servers'>('home');
  const [workbenchTab, setWorkbenchTab] = useState<'protocol' | 'servers' | 'android'>('android');
  const [pinging, setPinging] = useState(false);
  const [busyAction, setBusyAction] = useState<string | null>(null);

  const fetchState = useCallback(async () => {
    try {
      const res = await fetch('/api/state');
      if (res.ok) {
        const data: AppStateSnapshot = await res.json();
        setState(data);
      }
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    fetchState();
  }, [fetchState]);

  const handleToggleVpn = async (protocol?: string) => {
    setBusyAction('vpn-toggle');
    try {
      await fetch('/api/vpn/toggle', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(protocol ? { connect: true, protocol } : {}),
      });
      await fetchState();
    } finally {
      setBusyAction(null);
    }
  };

  const handleSelectAccount = async (id: string) => {
    setBusyAction(`acc-${id}`);
    try {
      await fetch(`/api/accounts/${id}/activate`, { method: 'POST' });
      await fetchState();
    } finally {
      setBusyAction(null);
    }
  };

  const handleToggleAccountEnabled = async (id: string) => {
    await fetch(`/api/accounts/${id}/toggle`, { method: 'POST' });
    await fetchState();
  };

  const handleMoveAccount = async (id: string, delta: number) => {
    await fetch(`/api/accounts/${id}/move`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ delta }),
    });
    await fetchState();
  };

  const handleDeleteAccount = async (id: string) => {
    await fetch(`/api/accounts/${id}`, { method: 'DELETE' });
    await fetchState();
  };

  const handleAddAccount = async (payload: {
    email: string;
    password?: string;
    sessionToken?: string;
    method: 'api_pbkdf2' | 'browser_webchannel';
  }) => {
    const res = await fetch('/api/accounts/add', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(payload),
    });
    if (!res.ok) {
      const err = await res.json().catch(() => ({ error: 'Ошибка добавления' }));
      throw new Error(err.error || 'Ошибка добавления');
    }
    await fetchState();
  };

  const handleChangeThreshold = async (thresholdPercent: number) => {
    await fetch('/api/accounts/threshold', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ thresholdPercent }),
    });
    await fetchState();
  };

  const handleSelectLocation = async (country: string) => {
    await fetch('/api/locations/select', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ country }),
    });
    await fetchState();
  };

  const handlePingLocations = async () => {
    setPinging(true);
    try {
      await fetch('/api/locations/ping', { method: 'POST' });
      await fetchState();
    } finally {
      setPinging(false);
    }
  };

  if (loading || !state) {
    return (
      <div className="min-h-screen bg-slate-950 text-slate-100 flex items-center justify-center p-6">
        <div className="text-sm font-mono text-slate-400">
          Загрузка Mozilla VPN Lite и синхронизация Remote Settings…
        </div>
      </div>
    );
  }

  const totalRemainingBytes = state.accounts.reduce(
    (sum, a) => sum + Math.max(0, a.usage.limitBytes - a.usage.usedBytes),
    0
  );

  return (
    <div className="min-h-screen bg-slate-950 text-slate-100 flex flex-col">
      {/* Strict 3-Zone Top Bar Contract */}
      <header className="flex items-center justify-between gap-8 px-6 py-4 border-b border-slate-800/80 bg-slate-950/90 sticky top-0 z-30 backdrop-blur-md">
        {/* Zone 1: Single text element wordmark */}
        <a
          href="#top"
          onClick={(e) => {
            e.preventDefault();
            setMobileScreen('home');
          }}
          className="text-lg font-bold tracking-tight text-white whitespace-nowrap shrink-0"
        >
          Mozilla VPN Lite
        </a>

        {/* Zone 2: 4 concise single-line navigation links */}
        <nav className="hidden md:flex items-center gap-6 text-sm font-medium text-slate-400">
          <a
            href="#remote"
            onClick={(e) => {
              e.preventDefault();
              setMobileScreen('home');
            }}
            className="hover:text-white hover:underline underline-offset-4 transition-colors whitespace-nowrap shrink-0"
          >
            Пульт и профили
          </a>
          <a
            href="#servers"
            onClick={(e) => {
              e.preventDefault();
              setMobileScreen('servers');
              setWorkbenchTab('servers');
            }}
            className="hover:text-white hover:underline underline-offset-4 transition-colors whitespace-nowrap shrink-0"
          >
            Выбор сервера
          </a>
          <a
            href="#protocol"
            onClick={(e) => {
              e.preventDefault();
              setWorkbenchTab('protocol');
            }}
            className="hover:text-white hover:underline underline-offset-4 transition-colors whitespace-nowrap shrink-0"
          >
            Протокол подключения
          </a>
          <a
            href="#android-code"
            onClick={(e) => {
              e.preventDefault();
              setWorkbenchTab('android');
            }}
            className="hover:text-white hover:underline underline-offset-4 transition-colors whitespace-nowrap shrink-0"
          >
            Исходники Android
          </a>
        </nav>

        {/* Zone 3: 1 primary action */}
        <div className="flex items-center shrink-0">
          <a
            href="/api/android/download.zip"
            download="mozilla-vpn-lite-android.zip"
            className="px-4 py-2 text-xs font-semibold text-white bg-indigo-600 rounded-lg hover:bg-indigo-500 transition-colors whitespace-nowrap shrink-0"
          >
            Скачать Android (.zip)
          </a>
        </div>
      </header>

      {/* Main Content Container (1440px Desktop Presence + Mobile Ergonomics) */}
      <main className="flex-1 w-full max-w-7xl mx-auto px-4 sm:px-6 py-6 flex flex-col gap-6">
        {/* Summary Strip (Unboxed metadata & tabular numbers) */}
        <section className="flex flex-wrap items-center justify-between gap-4 pb-4 border-b border-slate-800/80 text-xs text-slate-400">
          <div className="flex flex-wrap items-center gap-2 font-mono tabular-nums">
            <span className="text-slate-200 font-semibold">
              Активный аккаунт: {state.activeAccount?.email || 'не выбран'}
            </span>
            <span aria-hidden="true">·</span>
            <span>
              Суммарный остаток пула ({state.accounts.length}): {formatBytes(totalRemainingBytes)}
            </span>
            <span aria-hidden="true">·</span>
            <span>
              Выход: {state.effectiveLocation.nameRu} ({state.effectiveLocation.serverHost}:{state.effectiveLocation.serverPort})
            </span>
          </div>

          {state.lastSwitchEvent && (
            <div className="text-xs font-mono text-indigo-300 truncate max-w-xl">
              {state.lastSwitchEvent}
            </div>
          )}
        </section>

        {/* Responsive 2-Column Split: Left = Minimalist Android Client, Right = Protocol & Android Code */}
        <div className="grid grid-cols-1 lg:grid-cols-12 gap-8 items-start">
          {/* Left Column: Minimalist Android App Interface */}
          <div className="lg:col-span-5 flex flex-col gap-3">
            <div className="flex items-center justify-between text-xs text-slate-400 px-1">
              <span>Интерактивный Android-интерфейс (без лишних модулей)</span>
              <span className="font-mono">{state.tunInterface}</span>
            </div>

            <MinimalAndroidPhone
              state={state}
              mobileScreen={mobileScreen}
              onScreenChange={setMobileScreen}
              onToggleVpn={handleToggleVpn}
              onSelectAccount={handleSelectAccount}
              onToggleAccountEnabled={handleToggleAccountEnabled}
              onMoveAccount={handleMoveAccount}
              onDeleteAccount={handleDeleteAccount}
              onAddAccount={handleAddAccount}
              onChangeThreshold={handleChangeThreshold}
              onSelectLocation={handleSelectLocation}
              onPingLocations={handlePingLocations}
              pinging={pinging}
              busyAction={busyAction}
            />
          </div>

          {/* Right Column: Mozilla VPN Protocol Architecture, Server Table & Android Source Code */}
          <div className="lg:col-span-7">
            <ProtocolAndAndroidWorkbench
              state={state}
              activeTab={workbenchTab}
              onTabChange={setWorkbenchTab}
              onSelectLocation={handleSelectLocation}
              onPingAll={handlePingLocations}
              pinging={pinging}
            />
          </div>
        </div>
      </main>
    </div>
  );
}
