import React, { useState, useEffect } from 'react';
import {
  Download,
  Copy,
  Check,
  RefreshCw,
  KeyRound,
  Server,
  Code2,
  ShieldCheck,
  ArrowRight,
  Zap,
} from 'lucide-react';
import { AppStateSnapshot, CryptoDerivationResult } from '../types';
import { ANDROID_PROJECT_FILES, MOZILLA_VPN_PROTOCOL_STAGES } from '../data/androidProjectFiles';
import { countryFlag } from '../utils/format';

interface Props {
  state: AppStateSnapshot;
  activeTab: 'protocol' | 'servers' | 'android';
  onTabChange: (tab: 'protocol' | 'servers' | 'android') => void;
  onSelectLocation: (countryCode: string) => Promise<void>;
  onPingAll: () => Promise<void>;
  pinging: boolean;
}

export const ProtocolAndAndroidWorkbench: React.FC<Props> = ({
  state,
  activeTab,
  onTabChange,
  onSelectLocation,
  onPingAll,
  pinging,
}) => {
  const [selectedFileIndex, setSelectedFileIndex] = useState(0);
  const [copiedPath, setCopiedPath] = useState<string | null>(null);

  // Live FxA + Guardian Crypto Playground state
  const [testEmail, setTestEmail] = useState('rinat.main@firefox.com');
  const [testPassword, setTestPassword] = useState('mozilla-vpn-secret-2026');
  const [testSessionToken, setTestSessionToken] = useState('');
  const [cryptoResult, setCryptoResult] = useState<CryptoDerivationResult | null>(null);
  const [deriving, setDeriving] = useState(false);

  // GitHub Auto-Push & Cloud APK Build state
  const [ghToken, setGhToken] = useState('');
  const [ghRepoName, setGhRepoName] = useState('mozilla-vpn-lite');
  const [ghPrivate, setGhPrivate] = useState(false);
  const [ghPushing, setGhPushing] = useState(false);
  const [ghError, setGhError] = useState<string | null>(null);
  const [ghSuccess, setGhSuccess] = useState<{
    repoUrl: string;
    actionsUrl: string;
    releasesUrl: string;
  } | null>(null);

  const handleGitHubPush = async (e: React.FormEvent) => {
    e.preventDefault();
    setGhError(null);
    setGhSuccess(null);
    if (!ghToken.trim()) {
      setGhError('Вставьте GitHub Personal Access Token (с правами repo и workflow)');
      return;
    }
    setGhPushing(true);
    try {
      const res = await fetch('/api/github/push', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          token: ghToken.trim(),
          repoName: ghRepoName.trim() || 'mozilla-vpn-lite',
          isPrivate: ghPrivate,
        }),
      });
      const data = await res.json();
      if (!res.ok) {
        setGhError(data.error || 'Не удалось выгрузить проект в GitHub');
      } else {
        setGhSuccess({
          repoUrl: data.repoUrl,
          actionsUrl: data.actionsUrl,
          releasesUrl: data.releasesUrl,
        });
      }
    } catch (err: any) {
      setGhError(err?.message || 'Ошибка соединения с сервером');
    } finally {
      setGhPushing(false);
    }
  };

  const runCryptoDerive = async () => {
    setDeriving(true);
    try {
      const res = await fetch('/api/protocol/derive', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          email: testEmail,
          password: testPassword,
          sessionToken: testSessionToken || undefined,
        }),
      });
      if (res.ok) {
        const data = await res.json();
        setCryptoResult(data);
      }
    } finally {
      setDeriving(false);
    }
  };

  useEffect(() => {
    runCryptoDerive();
  }, []);

  const handleCopyCode = (path: string, content: string) => {
    navigator.clipboard.writeText(content);
    setCopiedPath(path);
    setTimeout(() => setCopiedPath(null), 2000);
  };

  const currentFile = ANDROID_PROJECT_FILES[selectedFileIndex] || ANDROID_PROJECT_FILES[0];

  return (
    <div className="flex flex-col gap-6">
      {/* Segmented Interactive Control Bar */}
      <div className="flex flex-wrap items-center justify-between gap-4 border-b border-slate-200 dark:border-slate-800 pb-4">
        <div className="flex items-center gap-1 p-1 bg-slate-100 dark:bg-slate-900 rounded-xl border border-slate-200 dark:border-slate-800">
          <button
            type="button"
            onClick={() => onTabChange('protocol')}
            className={`px-3.5 py-2 text-xs font-semibold rounded-lg transition-colors whitespace-nowrap shrink-0 min-h-[40px] flex items-center gap-2 ${
              activeTab === 'protocol'
                ? 'bg-white dark:bg-slate-800 text-slate-900 dark:text-white shadow-xs'
                : 'text-slate-600 dark:text-slate-400 hover:text-slate-900 dark:hover:text-slate-200'
            }`}
          >
            <KeyRound className="w-3.5 h-3.5" />
            <span>Как работает протокол</span>
          </button>
          <button
            type="button"
            onClick={() => onTabChange('servers')}
            className={`px-3.5 py-2 text-xs font-semibold rounded-lg transition-colors whitespace-nowrap shrink-0 min-h-[40px] flex items-center gap-2 ${
              activeTab === 'servers'
                ? 'bg-white dark:bg-slate-800 text-slate-900 dark:text-white shadow-xs'
                : 'text-slate-600 dark:text-slate-400 hover:text-slate-900 dark:hover:text-slate-200'
            }`}
          >
            <Server className="w-3.5 h-3.5" />
            <span>{`Таблица серверов\u00A0\u00A0(${state.locations.length})`}</span>
          </button>
          <button
            type="button"
            onClick={() => onTabChange('android')}
            className={`px-3.5 py-2 text-xs font-semibold rounded-lg transition-colors whitespace-nowrap shrink-0 min-h-[40px] flex items-center gap-2 ${
              activeTab === 'android'
                ? 'bg-white dark:bg-slate-800 text-slate-900 dark:text-white shadow-xs'
                : 'text-slate-600 dark:text-slate-400 hover:text-slate-900 dark:hover:text-slate-200'
            }`}
          >
            <Code2 className="w-3.5 h-3.5" />
            <span>Код Android (Kotlin + Go)</span>
          </button>
        </div>

        <a
          href="/api/android/download.zip"
          download="mozilla-vpn-lite-android.zip"
          className="px-4 py-2 text-xs font-semibold text-white bg-indigo-600 hover:bg-indigo-500 rounded-xl transition-colors whitespace-nowrap shrink-0 min-h-[40px] inline-flex items-center gap-2"
        >
          <Download className="w-3.5 h-3.5" />
          Скачать Android-проект (.zip)
        </a>
      </div>

      {/* TAB 1: MOZILLA VPN PROTOCOL ARCHITECTURE & LIVE CRYPTO INSPECTOR */}
      {activeTab === 'protocol' && (
        <div className="flex flex-col gap-6">
          <div className="bg-white dark:bg-slate-900 border border-slate-200 dark:border-slate-800 rounded-2xl p-6">
            <div className="flex flex-col gap-2 mb-5">
              <div className="text-xs text-slate-500 dark:text-slate-400">
                Реверс-инжиниринг репозитория armanlife/arman-vpn-client · Без WireGuard · Туннель через Firefox IP Protection (Guardian)
              </div>
              <h2 className="text-xl font-bold text-slate-900 dark:text-white tracking-tight">
                Механика подключения к Mozilla VPN и почему удалено всё лишнее
              </h2>
              <p className="text-sm text-slate-600 dark:text-slate-300 leading-relaxed">
                В отличие от классического платного клиента Mozilla VPN (WireGuard), <code className="text-indigo-600 dark:text-indigo-400 font-mono">arman-vpn-client</code> использует инфраструктуру встроенного прокси Firefox (<strong>Guardian / Firefox Private Network</strong>) и мультиплексированные туннели <strong>HTTP/2 CONNECT и HTTP/3 MASQUE</strong> через серверы Fastly (<code className="font-mono">*.m1.fastly-masque.net:2499</code>). Каждый бесплатный аккаунт Mozilla получает <strong>5 ГБ трафика в месяц</strong>, а пул аккаунтов автоматически переключает их без разрыва соединения.
              </p>
            </div>

            <div className="divide-y divide-slate-200 dark:divide-slate-800">
              {MOZILLA_VPN_PROTOCOL_STAGES.map((stage) => (
                <div key={stage.step} className="py-5 first:pt-2 last:pb-0 flex flex-col gap-3">
                  <div className="flex flex-wrap items-baseline justify-between gap-2">
                    <h3 className="text-base font-semibold text-slate-900 dark:text-white">
                      {stage.step}. {stage.title}
                    </h3>
                    <div className="text-xs font-mono text-slate-500 dark:text-slate-400">
                      <span>{stage.method}</span>
                      <span className="mx-1.5" aria-hidden="true">·</span>
                      <span className="text-indigo-600 dark:text-indigo-400">{stage.endpoint}</span>
                    </div>
                  </div>
                  <p className="text-sm text-slate-600 dark:text-slate-300 leading-relaxed">
                    {stage.summary}
                  </p>
                  <ul className="space-y-1.5 text-xs text-slate-500 dark:text-slate-400 list-disc pl-4">
                    {stage.details.map((detail, i) => (
                      <li key={i} className="leading-relaxed">{detail}</li>
                    ))}
                  </ul>
                  <pre className="mt-1 p-3.5 rounded-xl bg-slate-950 text-slate-200 text-xs font-mono overflow-x-auto border border-slate-800">
                    <code>{stage.codeSnippet}</code>
                  </pre>
                </div>
              ))}
            </div>
          </div>

          {/* Live FxA PBKDF2 + HKDF + HAWK + Guardian JWT Inspector */}
          <div className="bg-white dark:bg-slate-900 border border-slate-200 dark:border-slate-800 rounded-2xl p-6">
            <div className="flex flex-wrap items-center justify-between gap-4 mb-4">
              <div>
                <h3 className="text-base font-bold text-slate-900 dark:text-white">
                  Живой крипто-стенд FxA (PBKDF2 → HKDF → HAWK → Guardian ProxyPass)
                </h3>
                <p className="text-xs text-slate-500 dark:text-slate-400 mt-0.5">
                  Проверьте реальную генерацию ключей по алгоритму из <code className="font-mono">core/internal/fxa/hawk.go</code> на сервере
                </p>
              </div>
              <button
                type="button"
                onClick={runCryptoDerive}
                disabled={deriving}
                className="px-3.5 py-2 text-xs font-semibold text-white bg-slate-900 dark:bg-indigo-600 hover:bg-slate-800 dark:hover:bg-indigo-500 rounded-xl transition-colors whitespace-nowrap shrink-0 min-h-[40px] flex items-center gap-2"
              >
                <RefreshCw className={`w-3.5 h-3.5 ${deriving ? 'animate-spin' : ''}`} />
                Пересчитать ключи
              </button>
            </div>

            <div className="grid grid-cols-1 md:grid-cols-3 gap-3 mb-4">
              <div>
                <label className="block text-xs font-medium text-slate-600 dark:text-slate-400 mb-1">
                  Email аккаунта Mozilla
                </label>
                <input
                  type="text"
                  value={testEmail}
                  onChange={(e) => setTestEmail(e.target.value)}
                  className="w-full px-3 py-2 text-xs font-mono rounded-xl bg-slate-50 dark:bg-slate-950 border border-slate-200 dark:border-slate-800 text-slate-900 dark:text-slate-100 focus:outline-none focus:border-indigo-500"
                />
              </div>
              <div>
                <label className="block text-xs font-medium text-slate-600 dark:text-slate-400 mb-1">
                  Пароль (для PBKDF2 quickStretch)
                </label>
                <input
                  type="text"
                  value={testPassword}
                  onChange={(e) => setTestPassword(e.target.value)}
                  className="w-full px-3 py-2 text-xs font-mono rounded-xl bg-slate-50 dark:bg-slate-950 border border-slate-200 dark:border-slate-800 text-slate-900 dark:text-slate-100 focus:outline-none focus:border-indigo-500"
                />
              </div>
              <div>
                <label className="block text-xs font-medium text-slate-600 dark:text-slate-400 mb-1">
                  SessionToken (64-hex, опционально)
                </label>
                <input
                  type="text"
                  placeholder="Авто из authPW или вставьте 64-hex"
                  value={testSessionToken}
                  onChange={(e) => setTestSessionToken(e.target.value)}
                  className="w-full px-3 py-2 text-xs font-mono rounded-xl bg-slate-50 dark:bg-slate-950 border border-slate-200 dark:border-slate-800 text-slate-900 dark:text-slate-100 focus:outline-none focus:border-indigo-500"
                />
              </div>
            </div>

            {cryptoResult && (
              <div className="space-y-2.5 text-xs font-mono bg-slate-950 text-slate-300 p-4 rounded-xl border border-slate-800 overflow-x-auto">
                <div className="flex flex-col sm:flex-row sm:items-baseline gap-1 sm:gap-3">
                  <span className="text-slate-500 shrink-0 w-44">PBKDF2 Salt (1000 rds):</span>
                  <span className="text-slate-200 break-all">{cryptoResult.pbkdf2Salt}</span>
                </div>
                <div className="flex flex-col sm:flex-row sm:items-baseline gap-1 sm:gap-3">
                  <span className="text-slate-500 shrink-0 w-44">quickStretchedPW (32B):</span>
                  <span className="text-amber-300 break-all">{cryptoResult.quickStretchedHex}</span>
                </div>
                <div className="flex flex-col sm:flex-row sm:items-baseline gap-1 sm:gap-3">
                  <span className="text-slate-500 shrink-0 w-44">HKDF authPW (32B):</span>
                  <span className="text-emerald-400 break-all">{cryptoResult.authPwHex}</span>
                </div>
                <div className="flex flex-col sm:flex-row sm:items-baseline gap-1 sm:gap-3">
                  <span className="text-slate-500 shrink-0 w-44">HKDF hawkID (session):</span>
                  <span className="text-indigo-400 break-all">{cryptoResult.hawkId}</span>
                </div>
                <div className="flex flex-col sm:flex-row sm:items-baseline gap-1 sm:gap-3">
                  <span className="text-slate-500 shrink-0 w-44">Authorization Header:</span>
                  <span className="text-slate-200 break-all">{cryptoResult.hawkAuthorizationHeader}</span>
                </div>
                <div className="flex flex-col sm:flex-row sm:items-baseline gap-1 sm:gap-3 pt-1 border-t border-slate-800/80">
                  <span className="text-slate-500 shrink-0 w-44">Guardian ProxyPass JWT:</span>
                  <span className="text-sky-400 break-all">{cryptoResult.proxyPassJwt}</span>
                </div>
              </div>
            )}
          </div>
        </div>
      )}

      {/* TAB 2: FULL SERVER LIST TABLE (REMOTE SETTINGS VPN-SERVERLIST) */}
      {activeTab === 'servers' && (
        <div className="bg-white dark:bg-slate-900 border border-slate-200 dark:border-slate-800 rounded-2xl p-6">
          <div className="flex flex-wrap items-center justify-between gap-4 mb-5">
            <div>
              <div className="text-xs text-slate-500 dark:text-slate-400">
                Источник: firefox.settings.services.mozilla.com · Коллекция vpn-serverlist · Все locked-локации разблокированы
              </div>
              <h2 className="text-lg font-bold text-slate-900 dark:text-white mt-0.5">
                Серверы Mozilla Fastly MASQUE / CONNECT ({state.locations.length} стран)
              </h2>
            </div>

            <button
              type="button"
              onClick={onPingAll}
              disabled={pinging}
              className="px-4 py-2 text-xs font-semibold text-white bg-indigo-600 hover:bg-indigo-500 disabled:opacity-60 rounded-xl transition-colors whitespace-nowrap shrink-0 min-h-[40px] flex items-center gap-2"
            >
              <RefreshCw className={`w-3.5 h-3.5 ${pinging ? 'animate-spin' : ''}`} />
              {pinging ? 'Измерение HTTP/2 CONNECT…' : 'Измерить реальный пинг'}
            </button>
          </div>

          {/* Quick Modes Row */}
          <div className="grid grid-cols-1 sm:grid-cols-2 gap-3 mb-5">
            <button
              type="button"
              onClick={() => onSelectLocation('')}
              className={`p-4 rounded-xl border text-left transition-colors flex items-center justify-between ${
                state.selectedLocationMode === ''
                  ? 'border-indigo-500 bg-indigo-500/10'
                  : 'border-slate-200 dark:border-slate-800 hover:bg-slate-50 dark:hover:bg-slate-800/50'
              }`}
            >
              <div>
                <div className="text-sm font-semibold text-slate-900 dark:text-white">
                  Рекомендуемая локация (Anycast REC)
                </div>
                <div className="text-xs text-slate-500 dark:text-slate-400 mt-0.5">
                  Автовыбор ближайшего узла Fastly по умолчанию
                </div>
              </div>
              <ArrowRight className="w-4 h-4 text-slate-400 shrink-0" />
            </button>

            <button
              type="button"
              onClick={() => onSelectLocation('fastest')}
              className={`p-4 rounded-xl border text-left transition-colors flex items-center justify-between ${
                state.selectedLocationMode === 'fastest'
                  ? 'border-indigo-500 bg-indigo-500/10'
                  : 'border-slate-200 dark:border-slate-800 hover:bg-slate-50 dark:hover:bg-slate-800/50'
              }`}
            >
              <div>
                <div className="text-sm font-semibold text-slate-900 dark:text-white flex items-center gap-1.5">
                  <Zap className="w-4 h-4 text-amber-500" />
                  Самый быстрый сервер (Fastest)
                </div>
                <div className="text-xs text-slate-500 dark:text-slate-400 mt-0.5">
                  Минимальная задержка туннеля до www.google.com:443
                </div>
              </div>
              <ArrowRight className="w-4 h-4 text-slate-400 shrink-0" />
            </button>
          </div>

          <div className="overflow-x-auto">
            <table className="w-full text-left border-collapse">
              <thead>
                <tr className="border-b border-slate-200 dark:border-slate-800 text-xs text-slate-500 dark:text-slate-400">
                  <th className="py-2.5 pr-4 font-medium">Страна и город</th>
                  <th className="py-2.5 px-4 font-medium">Хост Fastly MASQUE</th>
                  <th className="py-2.5 px-4 font-medium">Статус в Firefox</th>
                  <th className="py-2.5 pl-4 text-right font-medium">Задержка</th>
                </tr>
              </thead>
              <tbody className="divide-y divide-slate-100 dark:divide-slate-800/70 text-sm">
                {state.locations.map((loc) => {
                  const isSelected =
                    state.selectedLocationMode.toUpperCase() === loc.code.toUpperCase() ||
                    (state.effectiveLocation.code === loc.code &&
                      (state.selectedLocationMode === '' || state.selectedLocationMode === 'fastest'));

                  return (
                    <tr
                      key={loc.code}
                      onClick={() => onSelectLocation(loc.code)}
                      className={`cursor-pointer transition-colors ${
                        isSelected
                          ? 'bg-indigo-500/10 dark:bg-indigo-500/15'
                          : 'hover:bg-slate-50 dark:hover:bg-slate-800/40'
                      }`}
                    >
                      <td className="py-3 pr-4">
                        <div className="flex items-center gap-2.5">
                          <span className="text-lg leading-none">{countryFlag(loc.code)}</span>
                          <div>
                            <div className="font-semibold text-slate-900 dark:text-white flex items-center gap-2">
                              <span>{loc.nameRu}</span>
                              <span className="text-xs font-mono text-slate-400">{loc.code}</span>
                            </div>
                            <div className="text-xs text-slate-500 dark:text-slate-400">{loc.city}</div>
                          </div>
                        </div>
                      </td>
                      <td className="py-3 px-4 font-mono text-xs text-slate-600 dark:text-slate-300">
                        {loc.serverHost}:{loc.serverPort}
                      </td>
                      <td className="py-3 px-4 text-xs text-slate-500 dark:text-slate-400">
                        {loc.lockedInFirefox
                          ? 'Locked в браузере · Открыт в клиенте'
                          : 'Доступен стандартно'}
                      </td>
                      <td className="py-3 pl-4 text-right font-mono text-xs tabular-nums">
                        <span
                          className={
                            loc.pingMs < 45
                              ? 'text-emerald-600 dark:text-emerald-400 font-semibold'
                              : loc.pingMs < 110
                              ? 'text-amber-600 dark:text-amber-400 font-semibold'
                              : 'text-slate-500 dark:text-slate-400'
                          }
                        >
                          {loc.pingMs} мс
                        </span>
                      </td>
                    </tr>
                  );
                })}
              </tbody>
            </table>
          </div>
        </div>
      )}

      {/* TAB 3: STRIPPED-DOWN ANDROID SOURCE CODE & GITHUB CLOUD APK BUILDER */}
      {activeTab === 'android' && (
        <div className="flex flex-col gap-6">
          {/* GitHub Auto-Push & Cloud APK Builder Card */}
          <div className="bg-white dark:bg-slate-900 border border-slate-200 dark:border-slate-800 rounded-2xl p-6 flex flex-col gap-4">
            <div>
              <div className="text-xs text-slate-500 dark:text-slate-400">
                Облачная сборка APK без установки Android Studio, Go и NDK на компьютер
              </div>
              <h2 className="text-lg font-bold text-slate-900 dark:text-white mt-0.5">
                Автовыгрузка в ваш GitHub и запуск сборки APK
              </h2>
              <p className="text-xs text-slate-500 dark:text-slate-400 mt-1 leading-relaxed">
                В проект встроено <strong>полностью переписанное с нуля Go-ядро</strong> (<code className="font-mono text-indigo-400">core/</code> — всего ~1 180 строк вместо 14 500+ в оригинале, удалены все модули роутеров, Windows WFP, автообновлений, подписи релизов и веб-UI) и пайплайн <code className="font-mono text-indigo-400">.github/workflows/build-apk.yml</code>. Вставьте свой GitHub Personal Access Token (с галочками <code className="font-mono">repo</code> и <code className="font-mono">workflow</code>) — сервер создаст репозиторий, выгрузит переписанное ядро и запустит компиляцию <code className="font-mono">Mozilla-VPN-Lite-arm64-v8a.apk</code>.
              </p>
            </div>

            <form onSubmit={handleGitHubPush} className="grid grid-cols-1 md:grid-cols-12 gap-3 items-end">
              <div className="md:col-span-5">
                <label className="block text-xs font-medium text-slate-600 dark:text-slate-400 mb-1">
                  GitHub Token (ghp_… / github_pat_…)
                </label>
                <input
                  type="password"
                  placeholder="ghp_xxxxxxxxxxxxxxxxxxxx"
                  value={ghToken}
                  onChange={(e) => setGhToken(e.target.value)}
                  className="w-full px-3 py-2.5 text-xs font-mono rounded-xl bg-slate-50 dark:bg-slate-950 border border-slate-200 dark:border-slate-800 text-slate-900 dark:text-slate-100 focus:outline-none focus:border-indigo-500"
                />
              </div>

              <div className="md:col-span-3">
                <label className="block text-xs font-medium text-slate-600 dark:text-slate-400 mb-1">
                  Имя репозитория
                </label>
                <input
                  type="text"
                  value={ghRepoName}
                  onChange={(e) => setGhRepoName(e.target.value)}
                  className="w-full px-3 py-2.5 text-xs font-mono rounded-xl bg-slate-50 dark:bg-slate-950 border border-slate-200 dark:border-slate-800 text-slate-900 dark:text-slate-100 focus:outline-none focus:border-indigo-500"
                />
              </div>

              <div className="md:col-span-4 flex items-center gap-2">
                <button
                  type="submit"
                  disabled={ghPushing}
                  className="w-full px-4 py-2.5 text-xs font-semibold text-white bg-indigo-600 hover:bg-indigo-500 disabled:opacity-60 rounded-xl transition-colors cursor-pointer whitespace-nowrap min-h-[40px] flex items-center justify-center gap-2"
                >
                  <RefreshCw className={`w-3.5 h-3.5 ${ghPushing ? 'animate-spin' : ''}`} />
                  {ghPushing ? 'Выгрузка и запуск…' : 'Загрузить в GitHub и собрать APK'}
                </button>
              </div>
            </form>

            <div className="flex items-center justify-between text-xs text-slate-500 dark:text-slate-400">
              <label className="inline-flex items-center gap-2 cursor-pointer">
                <input
                  type="checkbox"
                  checked={ghPrivate}
                  onChange={(e) => setGhPrivate(e.target.checked)}
                  className="rounded accent-indigo-500"
                />
                <span>Создать приватный репозиторий (Private)</span>
              </label>
              <a
                href="https://github.com/settings/tokens/new?scopes=repo,workflow&description=Mozilla+VPN+Lite+Builder"
                target="_blank"
                rel="noreferrer"
                className="text-indigo-400 hover:underline"
              >
                Создать токен в GitHub (1 клик) →
              </a>
            </div>

            {ghError && (
              <div className="p-3 rounded-xl bg-rose-500/10 border border-rose-500/30 text-xs text-rose-400">
                {ghError}
              </div>
            )}

            {ghSuccess && (
              <div className="p-4 rounded-xl bg-emerald-500/10 border border-emerald-500/30 text-xs text-emerald-300 flex flex-col gap-2">
                <div className="font-semibold text-white">
                  Репозиторий создан и сборка Android APK запущена в GitHub Actions!
                </div>
                <div className="flex flex-wrap items-center gap-4 font-mono">
                  <a
                    href={ghSuccess.actionsUrl}
                    target="_blank"
                    rel="noreferrer"
                    className="text-indigo-300 hover:underline font-semibold"
                  >
                    1. Открыть ход сборки (GitHub Actions) →
                  </a>
                  <a
                    href={ghSuccess.releasesUrl}
                    target="_blank"
                    rel="noreferrer"
                    className="text-emerald-300 hover:underline font-semibold"
                  >
                    2. Скачать готовый APK в Releases (через ~4 мин) →
                  </a>
                </div>
              </div>
            )}
          </div>

          <div className="bg-white dark:bg-slate-900 border border-slate-200 dark:border-slate-800 rounded-2xl p-6 flex flex-col gap-5">
            <div className="flex flex-wrap items-start justify-between gap-4">
              <div>
                <div className="text-xs text-slate-500 dark:text-slate-400">
                  Переписанное с нуля Go-ядро (mozillavpnlite/core) + Android UI · Без OpenWrt, Windows WFP, автообновлений и телеметрии
                </div>
                <h2 className="text-lg font-bold text-slate-900 dark:text-white mt-0.5">
                  Исходный код переписанного Go-ядра и Android-клиента
                </h2>
              </div>
            <button
              type="button"
              onClick={() => handleCopyCode(currentFile.path, currentFile.content)}
              className="px-3.5 py-2 text-xs font-semibold text-slate-700 dark:text-slate-200 bg-slate-100 dark:bg-slate-800 hover:bg-slate-200 dark:hover:bg-slate-700 rounded-xl transition-colors whitespace-nowrap shrink-0 min-h-[40px] flex items-center gap-2"
            >
              {copiedPath === currentFile.path ? (
                <>
                  <Check className="w-3.5 h-3.5 text-emerald-500" />
                  Скопировано
                </>
              ) : (
                <>
                  <Copy className="w-3.5 h-3.5" />
                  Копировать файл
                </>
              )}
            </button>
          </div>

          {/* File Selector Tabs */}
          <div className="flex flex-wrap items-center gap-1.5">
            {ANDROID_PROJECT_FILES.map((file, idx) => (
              <button
                key={file.path}
                type="button"
                onClick={() => setSelectedFileIndex(idx)}
                className={`px-3 py-1.5 text-xs font-mono rounded-lg transition-colors whitespace-nowrap shrink-0 ${
                  selectedFileIndex === idx
                    ? 'bg-indigo-600 text-white font-semibold'
                    : 'bg-slate-100 dark:bg-slate-800/80 text-slate-600 dark:text-slate-400 hover:text-slate-900 dark:hover:text-white'
                }`}
              >
                {file.path.startsWith('core/') ? file.path : file.path.split('/').pop()}
              </button>
            ))}
          </div>

          <div className="text-xs text-slate-600 dark:text-slate-300 bg-slate-50 dark:bg-slate-950/60 p-3.5 rounded-xl border border-slate-200 dark:border-slate-800">
            <div className="font-mono text-indigo-600 dark:text-indigo-400 mb-1">{currentFile.path}</div>
            <div>{currentFile.description}</div>
          </div>

          <pre className="p-4 rounded-xl bg-slate-950 text-slate-200 text-xs font-mono overflow-x-auto border border-slate-800 max-h-[540px] leading-relaxed">
            <code>{currentFile.content}</code>
          </pre>

          <div className="pt-2 border-t border-slate-200 dark:border-slate-800 flex flex-wrap items-center justify-between gap-4 text-xs text-slate-500 dark:text-slate-400">
            <div className="flex items-center gap-2">
              <ShieldCheck className="w-4 h-4 text-emerald-500 shrink-0" />
              <span>
                Все пакеты переписанного ядра сохранены в <code className="font-mono">/core</code> и <code className="font-mono">/android</code> и включены в ZIP-архив
              </span>
            </div>
            <a
              href="/api/android/download.zip"
              download="mozilla-vpn-lite-android.zip"
              className="text-indigo-600 dark:text-indigo-400 hover:underline font-medium"
            >
              Скачать полный архив ядра и Android Studio →
            </a>
          </div>
          </div>
        </div>
      )}
    </div>
  );
};
