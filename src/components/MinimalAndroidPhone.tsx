import React, { useState } from 'react';
import {
  Power,
  Users,
  Globe,
  SlidersHorizontal,
  Plus,
  ArrowUp,
  ArrowDown,
  Trash2,
  Check,
  RefreshCw,
  Zap,
  Search,
  Shield,
} from 'lucide-react';
import { AppStateSnapshot, MozillaAccount } from '../types';
import { countryFlag, formatBytes, formatShortDate } from '../utils/format';

interface Props {
  state: AppStateSnapshot;
  mobileScreen: 'home' | 'accounts' | 'servers';
  onScreenChange: (screen: 'home' | 'accounts' | 'servers') => void;
  onToggleVpn: (protocol?: string) => Promise<void>;
  onSelectAccount: (id: string) => Promise<void>;
  onToggleAccountEnabled: (id: string) => Promise<void>;
  onMoveAccount: (id: string, delta: number) => Promise<void>;
  onDeleteAccount: (id: string) => Promise<void>;
  onAddAccount: (payload: {
    email: string;
    password?: string;
    sessionToken?: string;
    method: 'api_pbkdf2' | 'browser_webchannel';
  }) => Promise<void>;
  onChangeThreshold: (percent: number) => Promise<void>;
  onSelectLocation: (code: string) => Promise<void>;
  onPingLocations: () => Promise<void>;
  pinging: boolean;
  busyAction: string | null;
}

export const MinimalAndroidPhone: React.FC<Props> = ({
  state,
  mobileScreen,
  onScreenChange,
  onToggleVpn,
  onSelectAccount,
  onToggleAccountEnabled,
  onMoveAccount,
  onDeleteAccount,
  onAddAccount,
  onChangeThreshold,
  onSelectLocation,
  onPingLocations,
  pinging,
  busyAction,
}) => {
  const [showAddForm, setShowAddForm] = useState(false);
  const [signInMethod, setSignInMethod] = useState<'api_pbkdf2' | 'browser_webchannel'>('api_pbkdf2');
  const [emailInput, setEmailInput] = useState('');
  const [passwordInput, setPasswordInput] = useState('');
  const [sessionTokenInput, setSessionTokenInput] = useState('');
  const [formError, setFormError] = useState<string | null>(null);
  const [searchQuery, setSearchQuery] = useState('');

  const loc = state.effectiveLocation;

  const handleCreateAccount = async (e: React.FormEvent) => {
    e.preventDefault();
    setFormError(null);
    if (!emailInput.trim() || !emailInput.includes('@')) {
      setFormError('Введите корректный email Firefox Account');
      return;
    }
    try {
      await onAddAccount({
        email: emailInput.trim(),
        password: passwordInput || 'mozilla-vpn-pass',
        sessionToken: sessionTokenInput || undefined,
        method: signInMethod,
      });
      setEmailInput('');
      setPasswordInput('');
      setSessionTokenInput('');
      setShowAddForm(false);
    } catch (err: any) {
      setFormError(err?.message || 'Ошибка добавления аккаунта');
    }
  };

  const filteredLocations = state.locations.filter((item) => {
    if (!searchQuery.trim()) return true;
    const q = searchQuery.toLowerCase();
    return (
      item.nameRu.toLowerCase().includes(q) ||
      item.name.toLowerCase().includes(q) ||
      item.code.toLowerCase().includes(q) ||
      item.city.toLowerCase().includes(q) ||
      item.serverHost.toLowerCase().includes(q)
    );
  });

  return (
    <div className="w-full bg-slate-900 text-slate-100 rounded-3xl border border-slate-800 shadow-xl overflow-hidden flex flex-col justify-between min-h-[680px]">
      {/* Android Minimal Top App Bar (52px compact height) */}
      <div className="px-5 h-13 border-b border-slate-800/80 flex items-center justify-between bg-slate-950/70">
        <div className="flex items-center gap-2.5 min-w-0">
          <Shield className="w-4 h-4 text-indigo-400 shrink-0" />
          <span className="text-sm font-bold tracking-tight text-white truncate">
            Mozilla VPN Lite
          </span>
        </div>
        <div className="text-xs font-mono text-slate-400 tabular-nums truncate">
          {state.vpnConnected
            ? `${countryFlag(loc.code)} ${loc.code} · ${loc.pingMs} мс`
            : 'Отключено'}
        </div>
      </div>

      {/* Main Scrollable Viewport Body */}
      <div className="flex-1 p-5 flex flex-col gap-5 overflow-y-auto">
        {/* ================================================================
            SCREEN 1: ПУЛЬТ БЫСТРОГО ПЕРЕКЛЮЧЕНИЯ (HOME)
           ================================================================ */}
        {mobileScreen === 'home' && (
          <>
            {/* Connection Hero Control */}
            <div className="bg-slate-950/90 border border-slate-800/90 rounded-2xl p-5 flex flex-col items-center text-center gap-4">
              <button
                type="button"
                onClick={() => onToggleVpn()}
                disabled={busyAction === 'vpn-toggle'}
                aria-label={state.vpnConnected ? 'Отключить VPN' : 'Подключить VPN'}
                className={`w-24 h-24 rounded-full flex items-center justify-center transition-transform active:scale-95 cursor-pointer border-2 ${
                  state.vpnConnected
                    ? 'bg-indigo-600 border-indigo-400 text-white shadow-lg shadow-indigo-600/25'
                    : 'bg-slate-900 border-slate-700 text-slate-400 hover:text-white hover:border-slate-500'
                }`}
              >
                <Power className="w-10 h-10" />
              </button>

              <div className="space-y-1">
                <div className="text-lg font-bold text-white">
                  {state.vpnConnected
                    ? `Подключено · ${loc.nameRu}`
                    : 'Туннель отключён'}
                </div>
                <div className="text-xs font-mono text-slate-400">
                  {loc.serverHost}:{loc.serverPort} · { loc.pingMs } мс
                </div>
              </div>

              {/* Protocol Segmented Switch (HTTP/3 MASQUE vs HTTP/2 CONNECT) */}
              <div className="w-full grid grid-cols-2 gap-1 p-1 bg-slate-900 rounded-xl border border-slate-800">
                <button
                  type="button"
                  onClick={() => onToggleVpn('HTTP/3 MASQUE (QUIC)')}
                  className={`py-2 px-2.5 text-xs font-medium rounded-lg transition-colors whitespace-nowrap truncate ${
                    state.tunnelProtocol.includes('HTTP/3')
                      ? 'bg-slate-800 text-white font-semibold'
                      : 'text-slate-400 hover:text-slate-200'
                  }`}
                >
                  HTTP/3 MASQUE
                </button>
                <button
                  type="button"
                  onClick={() => onToggleVpn('HTTP/2 CONNECT (TLS 1.3)')}
                  className={`py-2 px-2.5 text-xs font-medium rounded-lg transition-colors whitespace-nowrap truncate ${
                    state.tunnelProtocol.includes('HTTP/2')
                      ? 'bg-slate-800 text-white font-semibold'
                      : 'text-slate-400 hover:text-slate-200'
                  }`}
                >
                  HTTP/2 CONNECT
                </button>
              </div>
            </div>

            {/* Instant 1-Tap Mozilla Profile Switcher */}
            <div className="flex flex-col gap-2">
                {state.accounts.map((acc) => {
                  const usedPct = Math.min(
                    100,
                    Math.round((acc.usage.usedBytes / acc.usage.limitBytes) * 100)
                  );
                  const remainingBytes = Math.max(0, acc.usage.limitBytes - acc.usage.usedBytes);

                  return (
                    <button
                      key={acc.id}
                      type="button"
                      onClick={() => onSelectAccount(acc.id)}
                      className={`w-full p-3.5 rounded-2xl border text-left transition-colors cursor-pointer min-h-[60px] flex flex-col gap-2 ${
                        acc.active
                          ? 'bg-indigo-600/15 border-indigo-500/80 text-white'
                          : 'bg-slate-950/60 border-slate-800/90 text-slate-300 hover:border-slate-700'
                      }`}
                    >
                      <div className="flex items-center justify-between gap-2">
                        <div className="flex items-center gap-2 min-w-0">
                          <span className="text-xs font-mono text-slate-400 tabular-nums">
                            #{acc.position}
                          </span>
                          <span className="text-sm font-semibold truncate">
                            {acc.email}
                          </span>
                        </div>
                        <span className="text-xs font-mono shrink-0 text-slate-400 tabular-nums">
                          {acc.active
                            ? 'Активен'
                            : acc.status === 'reserved'
                            ? 'Квота исчерпана'
                            : 'Готов'}
                        </span>
                      </div>

                      {/* Minimal Quota Progress Bar */}
                      <div className="w-full h-1.5 bg-slate-800 rounded-full overflow-hidden">
                        <div
                          className={`h-full transition-all ${
                            usedPct >= state.thresholdPercent
                              ? 'bg-rose-500'
                              : usedPct >= 75
                              ? 'bg-amber-400'
                              : 'bg-indigo-500'
                          }`}
                          style={{ width: `${usedPct}%` }}
                        />
                      </div>

                      <div className="flex items-center justify-between text-[11px] font-mono text-slate-400 tabular-nums">
                        <span>
                          Осталось: {formatBytes(remainingBytes)} из {formatBytes(acc.usage.limitBytes)}
                        </span>
                        <span>{usedPct}% · порог {state.thresholdPercent}%</span>
                      </div>
                    </button>
                  );
                })}
              </div>

            {/* Instant 1-Tap Server / Location Switcher */}
            <div className="grid grid-cols-2 gap-2">
                <button
                  type="button"
                  onClick={() => onSelectLocation('fastest')}
                  className={`p-3 rounded-xl border text-left transition-colors cursor-pointer min-h-[52px] flex items-center justify-between ${
                    state.selectedLocationMode === 'fastest'
                      ? 'bg-indigo-600/15 border-indigo-500 text-white'
                      : 'bg-slate-950/60 border-slate-800 text-slate-300 hover:border-slate-700'
                  }`}
                >
                  <div className="min-w-0">
                    <div className="text-xs font-semibold truncate flex items-center gap-1">
                      <Zap className="w-3.5 h-3.5 text-amber-400 shrink-0" />
                      Быстрейший
                    </div>
                    <div className="text-[11px] text-slate-400 font-mono truncate">
                      Авто пинг
                    </div>
                  </div>
                </button>

                {state.locations.slice(0, 5).map((item) => {
                  const selected =
                    state.selectedLocationMode.toUpperCase() === item.code.toUpperCase();
                  return (
                    <button
                      key={item.code}
                      type="button"
                      onClick={() => onSelectLocation(item.code)}
                      className={`p-3 rounded-xl border text-left transition-colors cursor-pointer min-h-[52px] flex items-center justify-between gap-2 ${
                        selected
                          ? 'bg-indigo-600/15 border-indigo-500 text-white'
                          : 'bg-slate-950/60 border-slate-800 text-slate-300 hover:border-slate-700'
                      }`}
                    >
                      <div className="flex items-center gap-2 min-w-0">
                        <span className="text-base leading-none shrink-0">
                          {countryFlag(item.code)}
                        </span>
                        <div className="min-w-0">
                          <div className="text-xs font-semibold truncate">
                            {item.nameRu}
                          </div>
                          <div className="text-[11px] font-mono text-slate-400 tabular-nums">
                            {item.pingMs} мс
                          </div>
                        </div>
                      </div>
                    </button>
                  );
                })}
            </div>
          </>
        )}

        {/* ================================================================
            SCREEN 2: АККАУНТЫ MOZILLA И АВТО-РОТАЦИЯ (ACCOUNTS)
           ================================================================ */}
        {mobileScreen === 'accounts' && (
          <div className="flex flex-col gap-4">
            <div className="flex items-center justify-between gap-2">
              <div>
                <h2 className="text-base font-bold text-white">
                  {`Профили Mozilla (${state.accounts.length})`}
                </h2>
                <p className="text-xs text-slate-400">
                  Каждый аккаунт даёт 5 ГБ/мес · Авто-смена по очереди
                </p>
              </div>
              <button
                type="button"
                onClick={() => setShowAddForm(!showAddForm)}
                className="px-3.5 py-2 text-xs font-semibold bg-indigo-600 hover:bg-indigo-500 text-white rounded-xl transition-colors cursor-pointer whitespace-nowrap shrink-0 min-h-[40px] flex items-center gap-1"
              >
                <Plus className="w-3.5 h-3.5" />
                Добавить
              </button>
            </div>

            {/* Add Mozilla Account Form (Direct API vs Browser WebChannel) */}
            {showAddForm && (
              <form
                onSubmit={handleCreateAccount}
                className="p-4 rounded-2xl bg-slate-950 border border-slate-800 flex flex-col gap-3"
              >
                <div className="text-xs font-semibold text-white">
                  Добавление аккаунта Firefox (Mozilla VPN)
                </div>

                <div className="grid grid-cols-2 gap-1 p-1 bg-slate-900 rounded-xl border border-slate-800">
                  <button
                    type="button"
                    onClick={() => setSignInMethod('api_pbkdf2')}
                    className={`py-1.5 px-2 text-xs font-medium rounded-lg transition-colors ${
                      signInMethod === 'api_pbkdf2'
                        ? 'bg-slate-800 text-white font-semibold'
                        : 'text-slate-400'
                    }`}
                  >
                    Прямой вход (PBKDF2)
                  </button>
                  <button
                    type="button"
                    onClick={() => setSignInMethod('browser_webchannel')}
                    className={`py-1.5 px-2 text-xs font-medium rounded-lg transition-colors ${
                      signInMethod === 'browser_webchannel'
                        ? 'bg-slate-800 text-white font-semibold'
                        : 'text-slate-400'
                    }`}
                  >
                    Браузер (SessionToken)
                  </button>
                </div>

                <input
                  type="email"
                  placeholder="Email аккаунта Mozilla (например, user@firefox.com)"
                  value={emailInput}
                  onChange={(e) => setEmailInput(e.target.value)}
                  className="w-full px-3 py-2.5 text-xs rounded-xl bg-slate-900 border border-slate-800 text-white focus:outline-none focus:border-indigo-500"
                />

                {signInMethod === 'api_pbkdf2' ? (
                  <input
                    type="password"
                    placeholder="Пароль (растягивается через PBKDF2 + HKDF authPW)"
                    value={passwordInput}
                    onChange={(e) => setPasswordInput(e.target.value)}
                    className="w-full px-3 py-2.5 text-xs rounded-xl bg-slate-900 border border-slate-800 text-white focus:outline-none focus:border-indigo-500"
                  />
                ) : (
                  <input
                    type="text"
                    placeholder="64-hex sessionToken из WebChannel (fxaccounts:login)"
                    value={sessionTokenInput}
                    onChange={(e) => setSessionTokenInput(e.target.value)}
                    className="w-full px-3 py-2.5 text-xs font-mono rounded-xl bg-slate-900 border border-slate-800 text-white focus:outline-none focus:border-indigo-500"
                  />
                )}

                {formError && (
                  <div className="text-xs text-rose-400">{formError}</div>
                )}

                <div className="flex items-center justify-end gap-2 pt-1">
                  <button
                    type="button"
                    onClick={() => setShowAddForm(false)}
                    className="px-3 py-2 text-xs text-slate-400 hover:text-white"
                  >
                    Отмена
                  </button>
                  <button
                    type="submit"
                    className="px-4 py-2 text-xs font-semibold bg-indigo-600 hover:bg-indigo-500 text-white rounded-xl"
                  >
                    Сохранить и получить ProxyPass
                  </button>
                </div>
              </form>
            )}

            {/* Account Cards with Priority Reorder & 1-Tap Switch */}
            <div className="flex flex-col gap-2.5">
              {state.accounts.map((acc: MozillaAccount, idx: number) => {
                const usedPct = Math.min(
                  100,
                  Math.round((acc.usage.usedBytes / acc.usage.limitBytes) * 100)
                );

                return (
                  <div
                    key={acc.id}
                    className={`p-4 rounded-2xl border flex flex-col gap-3 ${
                      acc.active
                        ? 'bg-indigo-600/15 border-indigo-500/80'
                        : 'bg-slate-950/70 border-slate-800'
                    }`}
                  >
                    <div className="flex items-start justify-between gap-2">
                      <div className="min-w-0">
                        <div className="flex items-center gap-2">
                          <span className="text-xs font-mono text-slate-400 tabular-nums">
                            #{acc.position}
                          </span>
                          <span className="text-sm font-bold text-white truncate">
                            {acc.email}
                          </span>
                        </div>
                        <div className="text-[11px] text-slate-400 mt-0.5 font-mono">
                          {acc.active ? 'Активный профиль' : acc.status === 'reserved' ? 'В резерве до сброса' : 'В очереди ротации'}
                          {' · '}
                          {acc.signInMethod === 'browser_webchannel' ? 'WebChannel' : 'PBKDF2 API'}
                        </div>
                      </div>

                      {!acc.active ? (
                        <button
                          type="button"
                          onClick={() => onSelectAccount(acc.id)}
                          className="px-3 py-1.5 text-xs font-semibold bg-indigo-600 hover:bg-indigo-500 text-white rounded-lg transition-colors whitespace-nowrap shrink-0 cursor-pointer"
                        >
                          Включить
                        </button>
                      ) : (
                        <span className="text-xs font-semibold text-indigo-300 flex items-center gap-1 shrink-0">
                          <Check className="w-3.5 h-3.5" />
                          Используется
                        </span>
                      )}
                    </div>

                    {/* Quota Bar */}
                    <div>
                      <div className="w-full h-1.5 bg-slate-800 rounded-full overflow-hidden mb-1.5">
                        <div
                          className={`h-full ${
                            usedPct >= state.thresholdPercent
                              ? 'bg-rose-500'
                              : 'bg-indigo-500'
                          }`}
                          style={{ width: `${usedPct}%` }}
                        />
                      </div>
                      <div className="flex items-center justify-between text-[11px] font-mono text-slate-400 tabular-nums">
                        <span>
                          {formatBytes(acc.usage.usedBytes)} / {formatBytes(acc.usage.limitBytes)} ({usedPct}%)
                        </span>
                        <span>Сброс {formatShortDate(acc.usage.resetAt)}</span>
                      </div>
                    </div>

                    {/* Bottom Controls: Priority Up/Down, Enable Switch, Delete */}
                    <div className="flex items-center justify-between pt-2 border-t border-slate-800/80 text-xs">
                      <div className="flex items-center gap-1">
                        <button
                          type="button"
                          disabled={idx === 0}
                          onClick={() => onMoveAccount(acc.id, -1)}
                          title="Повысить приоритет"
                          className="p-1.5 rounded-lg bg-slate-900 hover:bg-slate-800 disabled:opacity-30 text-slate-300 cursor-pointer"
                        >
                          <ArrowUp className="w-3.5 h-3.5" />
                        </button>
                        <button
                          type="button"
                          disabled={idx === state.accounts.length - 1}
                          onClick={() => onMoveAccount(acc.id, 1)}
                          title="Понизить приоритет"
                          className="p-1.5 rounded-lg bg-slate-900 hover:bg-slate-800 disabled:opacity-30 text-slate-300 cursor-pointer"
                        >
                          <ArrowDown className="w-3.5 h-3.5" />
                        </button>
                        <button
                          type="button"
                          onClick={() => onToggleAccountEnabled(acc.id)}
                          className="ml-1 px-2.5 py-1 rounded-lg bg-slate-900 hover:bg-slate-800 text-slate-300 font-medium cursor-pointer"
                        >
                          {acc.enabled ? 'В ротации' : 'Отключён'}
                        </button>
                      </div>

                      <button
                        type="button"
                        onClick={() => onDeleteAccount(acc.id)}
                        title="Удалить профиль"
                        className="p-1.5 rounded-lg text-slate-400 hover:text-rose-400 hover:bg-slate-900 cursor-pointer"
                      >
                        <Trash2 className="w-3.5 h-3.5" />
                      </button>
                    </div>
                  </div>
                );
              })}
            </div>

            {/* Auto-Switch Quota Threshold Slider (Matches _AccountSwitching in arman-vpn-client) */}
            <div className="p-4 rounded-2xl bg-slate-950/70 border border-slate-800 flex flex-col gap-2">
              <div className="flex items-center justify-between text-xs">
                <span className="font-semibold text-slate-200 flex items-center gap-1.5">
                  <SlidersHorizontal className="w-3.5 h-3.5 text-indigo-400" />
                  Порог авто-переключения аккаунта
                </span>
                <span className="font-mono font-bold text-indigo-400 tabular-nums">
                  {state.thresholdPercent}% квоты
                </span>
              </div>
              <input
                type="range"
                min={50}
                max={100}
                step={5}
                value={state.thresholdPercent}
                onChange={(e) => onChangeThreshold(Number(e.target.value))}
                className="w-full accent-indigo-500 cursor-pointer"
              />
              <div className="text-[11px] text-slate-400">
                При достижении {state.thresholdPercent}% из 5 ГБ ядро автоматически запрашивает ProxyPass следующего аккаунта в списке.
              </div>
            </div>
          </div>
        )}

        {/* ================================================================
            SCREEN 3: ВЫБОР СЕРВЕРА И ПИНГ (SERVERS)
           ================================================================ */}
        {mobileScreen === 'servers' && (
          <div className="flex flex-col gap-3.5">
            <div className="flex items-center justify-between gap-2">
              <div>
                <h2 className="text-base font-bold text-white">
                  {`Локации Mozilla (${state.locations.length})`}
                </h2>
                <p className="text-xs text-slate-400">
                  Все страны из vpn-serverlist разблокированы
                </p>
              </div>
              <button
                type="button"
                onClick={onPingLocations}
                disabled={pinging}
                className="px-3 py-2 text-xs font-semibold bg-indigo-600 hover:bg-indigo-500 disabled:opacity-60 text-white rounded-xl transition-colors cursor-pointer whitespace-nowrap shrink-0 min-h-[40px] flex items-center gap-1.5"
              >
                <RefreshCw className={`w-3.5 h-3.5 ${pinging ? 'animate-spin' : ''}`} />
                Пинг
              </button>
            </div>

            {/* Search Filter */}
            <div className="relative">
              <Search className="w-4 h-4 text-slate-500 absolute left-3.5 top-1/2 -translate-y-1/2" />
              <input
                type="text"
                placeholder="Поиск страны, города или Fastly-узла…"
                value={searchQuery}
                onChange={(e) => setSearchQuery(e.target.value)}
                className="w-full pl-9 pr-3.5 py-2.5 text-xs rounded-xl bg-slate-950 border border-slate-800 text-white placeholder:text-slate-500 focus:outline-none focus:border-indigo-500"
              />
            </div>

            {/* Automatic Selection Rows (Recommended & Fastest) */}
            <div className="grid grid-cols-2 gap-2">
              <button
                type="button"
                onClick={() => {
                  onSelectLocation('');
                  onScreenChange('home');
                }}
                className={`p-3 rounded-xl border text-left transition-colors cursor-pointer ${
                  state.selectedLocationMode === ''
                    ? 'bg-indigo-600/20 border-indigo-500 text-white'
                    : 'bg-slate-950/60 border-slate-800 text-slate-300'
                }`}
              >
                <div className="text-xs font-bold">Рекомендуемый</div>
                <div className="text-[11px] text-slate-400 mt-0.5">Anycast (REC)</div>
              </button>

              <button
                type="button"
                onClick={() => {
                  onSelectLocation('fastest');
                  onScreenChange('home');
                }}
                className={`p-3 rounded-xl border text-left transition-colors cursor-pointer ${
                  state.selectedLocationMode === 'fastest'
                    ? 'bg-indigo-600/20 border-indigo-500 text-white'
                    : 'bg-slate-950/60 border-slate-800 text-slate-300'
                }`}
              >
                <div className="text-xs font-bold flex items-center gap-1">
                  <Zap className="w-3.5 h-3.5 text-amber-400" />
                  Быстрейший
                </div>
                <div className="text-[11px] text-slate-400 mt-0.5">Мин. задержка</div>
              </button>
            </div>

            {/* Scrollable Country Tap List */}
            <div className="flex flex-col divide-y divide-slate-800/70 bg-slate-950/60 border border-slate-800 rounded-2xl overflow-hidden max-h-[380px] overflow-y-auto">
              {filteredLocations.map((item) => {
                const selected =
                  state.selectedLocationMode.toUpperCase() === item.code.toUpperCase();
                return (
                  <button
                    key={item.code}
                    type="button"
                    onClick={() => {
                      onSelectLocation(item.code);
                      onScreenChange('home');
                    }}
                    className={`w-full px-4 py-3 text-left flex items-center justify-between gap-3 transition-colors cursor-pointer min-h-[56px] ${
                      selected
                        ? 'bg-indigo-600/15 text-white'
                        : 'hover:bg-slate-900/90 text-slate-200'
                    }`}
                  >
                    <div className="flex items-center gap-3 min-w-0">
                      <span className="text-xl leading-none shrink-0">
                        {countryFlag(item.code)}
                      </span>
                      <div className="min-w-0">
                        <div className="text-sm font-semibold truncate">
                          {item.nameRu}
                        </div>
                        <div className="text-[11px] font-mono text-slate-400 truncate">
                          {item.city} · {item.serverHost}
                        </div>
                      </div>
                    </div>

                    <div className="text-right shrink-0 font-mono text-xs tabular-nums">
                      <span
                        className={
                          item.pingMs < 45
                            ? 'text-emerald-400 font-semibold'
                            : item.pingMs < 110
                            ? 'text-amber-400 font-semibold'
                            : 'text-slate-400'
                        }
                      >
                        {item.pingMs} мс
                      </span>
                    </div>
                  </button>
                );
              })}
            </div>
          </div>
        )}
      </div>

      {/* Android Bottom Thumb Navigation Bar (3 Core Destinations) */}
      <div className="grid grid-cols-3 border-t border-slate-800 bg-slate-950/90 h-16">
        <button
          type="button"
          onClick={() => onScreenChange('home')}
          className={`flex flex-col items-center justify-center gap-1 transition-colors cursor-pointer ${
            mobileScreen === 'home'
              ? 'text-indigo-400 font-semibold'
              : 'text-slate-400 hover:text-slate-200'
          }`}
        >
          <Power className="w-5 h-5" />
          <span className="text-[11px]">Пульт</span>
        </button>

        <button
          type="button"
          onClick={() => onScreenChange('accounts')}
          className={`flex flex-col items-center justify-center gap-1 transition-colors cursor-pointer ${
            mobileScreen === 'accounts'
              ? 'text-indigo-400 font-semibold'
              : 'text-slate-400 hover:text-slate-200'
          }`}
        >
          <Users className="w-5 h-5" />
          <span className="text-[11px]">
            {`Аккаунты\u00A0\u00A0(${state.accounts.length})`}
          </span>
        </button>

        <button
          type="button"
          onClick={() => onScreenChange('servers')}
          className={`flex flex-col items-center justify-center gap-1 transition-colors cursor-pointer ${
            mobileScreen === 'servers'
              ? 'text-indigo-400 font-semibold'
              : 'text-slate-400 hover:text-slate-200'
          }`}
        >
          <Globe className="w-5 h-5" />
          <span className="text-[11px]">
            {`Серверы\u00A0\u00A0(${state.locations.length})`}
          </span>
        </button>
      </div>
    </div>
  );
};
