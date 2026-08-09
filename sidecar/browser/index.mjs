import crypto from 'node:crypto';
import dns from 'node:dns/promises';
import fs from 'node:fs/promises';
import net from 'node:net';
import path from 'node:path';
import process from 'node:process';
import readline from 'node:readline';
import { chromium } from 'playwright';

const PROTOCOL_VERSION = '1.0';
const MAX_SESSIONS = 1;
const MAX_PAGES = 8;
const MAX_EVENTS = 200;
const MAX_EVENT_TEXT = 2048;
const MAX_SNAPSHOT_BYTES = 64 * 1024;
const MAX_SCREENSHOT_BYTES = 8 * 1024 * 1024;
const MAX_SCREENSHOT_PIXELS = 20_000_000;
const MAX_ACTION_TIMEOUT_MS = 30_000;

const dangerousIPs = new net.BlockList();
dangerousIPs.addSubnet('0.0.0.0', 8, 'ipv4');
dangerousIPs.addSubnet('169.254.0.0', 16, 'ipv4');
dangerousIPs.addSubnet('224.0.0.0', 4, 'ipv4');
dangerousIPs.addSubnet('240.0.0.0', 4, 'ipv4');
dangerousIPs.addAddress('100.100.100.200', 'ipv4');
dangerousIPs.addAddress('::', 'ipv6');
dangerousIPs.addSubnet('fe80::', 10, 'ipv6');
dangerousIPs.addSubnet('ff00::', 8, 'ipv6');

const privateIPs = new net.BlockList();
privateIPs.addSubnet('127.0.0.0', 8, 'ipv4');
privateIPs.addSubnet('10.0.0.0', 8, 'ipv4');
privateIPs.addSubnet('172.16.0.0', 12, 'ipv4');
privateIPs.addSubnet('192.168.0.0', 16, 'ipv4');
privateIPs.addSubnet('100.64.0.0', 10, 'ipv4');
privateIPs.addAddress('::1', 'ipv6');
privateIPs.addSubnet('fc00::', 7, 'ipv6');

const sessions = new Map();

class SidecarError extends Error {
  constructor(code, message, retryable = false) {
    super(message);
    this.code = code;
    this.retryable = retryable;
  }
}

function boundedText(value, limit = MAX_EVENT_TEXT) {
  const text = redactBrowserText(String(value ?? '').replace(/[\r\n]+/g, ' '));
  return text.length <= limit ? text : `${text.slice(0, limit)}…`;
}

function redactBrowserText(value) {
  return value
    .replace(/Bearer\s+[A-Za-z0-9._~+/=-]+/gi, 'Bearer [REDACTED]')
    .replace(/((?:authorization|api[_-]?key|token|secret|password)\s*[=:]\s*)[^\s,;&]+/gi, '$1[REDACTED]');
}

function safeEventURL(rawURL) {
  try {
    const parsed = new URL(rawURL);
    parsed.username = '';
    parsed.password = '';
    parsed.search = '';
    parsed.hash = '';
    return boundedText(parsed.toString(), 1024);
  } catch {
    return '[invalid-url]';
  }
}

function response(id, result) {
  process.stdout.write(`${JSON.stringify({ id, ok: true, result })}\n`);
}

function failure(id, error) {
  const code = error instanceof SidecarError ? error.code : 'BROWSER_ACTION_FAILED';
  const retryable = error instanceof SidecarError ? error.retryable : false;
  const message = boundedText(error?.message || error, 512);
  process.stdout.write(`${JSON.stringify({ id, ok: false, error: { code, message, retryable } })}\n`);
}

function requireString(value, name, max = 256) {
  if (typeof value !== 'string' || value.length < 1 || value.length > max || value.includes('\u0000')) {
    throw new SidecarError('INVALID_REQUEST', `${name} is invalid`);
  }
  return value;
}

function actionTimeout(params) {
  const raw = Number(params?.timeoutMs ?? 10_000);
  if (!Number.isFinite(raw) || raw < 100 || raw > MAX_ACTION_TIMEOUT_MS) {
    throw new SidecarError('INVALID_REQUEST', 'timeoutMs is outside the allowed range');
  }
  return Math.trunc(raw);
}

function waitUntilValue(params) {
  const value = params?.waitUntil || 'domcontentloaded';
  if (!['load', 'domcontentloaded', 'networkidle', 'commit'].includes(value)) {
    throw new SidecarError('INVALID_REQUEST', 'waitUntil is invalid');
  }
  return value;
}

function normalizedOrigin(rawURL) {
  let parsed;
  try {
    parsed = new URL(rawURL);
  } catch {
    throw new SidecarError('BROWSER_NETWORK_DENIED', 'request URL is invalid');
  }
  if (!['http:', 'https:', 'ws:', 'wss:'].includes(parsed.protocol)) {
    throw new SidecarError('BROWSER_NETWORK_DENIED', 'request scheme is not allowed');
  }
  const secure = parsed.protocol === 'https:' || parsed.protocol === 'wss:';
  const policyProtocol = secure ? 'https:' : 'http:';
  const port = parsed.port || (secure ? '443' : '80');
  let host = parsed.hostname.toLowerCase().replace(/\.$/, '');
  if (host.startsWith('[') && host.endsWith(']')) host = host.slice(1, -1);
  const bracketed = host.includes(':') ? `[${host}]` : host;
  return { origin: `${policyProtocol}//${bracketed}:${port}`, host };
}

async function resolvedIPs(host) {
  try {
    const records = await dns.lookup(host, { all: true, verbatim: true });
    const values = [...new Set(records.map(record => record.address))].sort();
    if (values.length === 0) throw new Error('no addresses');
    return values;
  } catch (error) {
    throw new SidecarError('BROWSER_DNS_FAILED', `DNS resolution failed: ${boundedText(error.message, 160)}`, true);
  }
}

function sameSet(a, b) {
  if (!Array.isArray(a) || !Array.isArray(b) || a.length !== b.length) return false;
  const left = [...a].sort();
  const right = [...b].sort();
  return left.every((value, index) => value === right[index]);
}

function resolverRulesForOrigins(allowedOrigins) {
  const rules = new Map();
  for (const rule of allowedOrigins) {
    const parsed = new URL(rule.origin);
    let host = parsed.hostname.toLowerCase().replace(/\.$/, '');
    if (host.startsWith('[') && host.endsWith(']')) host = host.slice(1, -1);
    if (net.isIP(host)) continue;
    const pinned = rule.pinnedIps.find(ip => net.isIP(ip) === 4) || rule.pinnedIps.find(ip => net.isIP(ip) === 6);
    if (!pinned) throw new SidecarError('INVALID_REQUEST', `origin ${rule.origin} has no valid pinned IP`);
    const replacement = net.isIP(pinned) === 6 ? `[${pinned}]` : pinned;
    const value = `MAP ${host} ${replacement}`;
    const existing = rules.get(host);
    if (existing && existing !== value) {
      throw new SidecarError('BROWSER_DNS_CHANGED', `authorized origins for ${host} have inconsistent pinned addresses`);
    }
    rules.set(host, value);
  }
  return [...rules.values()];
}

function blockListContains(list, address) {
  const family = net.isIPv4(address) ? 'ipv4' : net.isIPv6(address) ? 'ipv6' : '';
  return family === '' || list.check(address, family);
}

async function assertRequestAllowed(session, rawURL) {
  const { origin, host } = normalizedOrigin(rawURL);
  const current = await resolvedIPs(host);
  if (current.some(address => blockListContains(dangerousIPs, address))) {
    throw new SidecarError('BROWSER_NETWORK_DENIED', `origin ${origin} resolves to a blocked address`);
  }
  if (!current.some(address => blockListContains(privateIPs, address)) || session.allowPrivateNetwork) return;
  const rule = session.allowedOrigins.find(item => item.origin === origin);
  if (!rule) throw new SidecarError('BROWSER_NETWORK_DENIED', `local/private origin ${origin} is not authorized`);
  if (!sameSet(current, rule.pinnedIps)) {
    throw new SidecarError('BROWSER_DNS_CHANGED', `origin ${origin} no longer resolves to its pinned addresses`);
  }
}

function pushEvent(session, type, data = {}) {
  session.eventSequence += 1;
  session.events.push({ sequence: session.eventSequence, type, timestamp: new Date().toISOString(), ...data });
  if (session.events.length > MAX_EVENTS) session.events.splice(0, session.events.length - MAX_EVENTS);
}

function attachPage(session, page, preferredId = '') {
  for (const [id, current] of session.pages) {
    if (current === page) return id;
  }
  if (session.pages.size >= MAX_PAGES) {
    pushEvent(session, 'page_blocked', { reason: 'page limit reached' });
    void page.close().catch(() => {});
    return '';
  }
  const pageId = preferredId || `pg_${crypto.randomUUID().replaceAll('-', '')}`;
  session.pages.set(pageId, page);
  page.on('console', message => {
    pushEvent(session, 'console', { pageId, level: message.type(), text: boundedText(message.text()) });
  });
  page.on('pageerror', error => {
    pushEvent(session, 'page_error', { pageId, text: boundedText(error.message) });
  });
  page.on('requestfailed', request => {
    pushEvent(session, 'network_error', {
      pageId,
      url: safeEventURL(request.url()),
      method: request.method(),
      error: boundedText(request.failure()?.errorText || 'request failed', 256),
    });
  });
  page.on('close', () => session.pages.delete(pageId));
  return pageId;
}

function getSession(params) {
  const sessionId = requireString(params?.browserSessionId, 'browserSessionId', 128);
  const session = sessions.get(sessionId);
  if (!session) throw new SidecarError('BROWSER_SESSION_NOT_FOUND', 'browser session was not found');
  session.lastUsedAt = Date.now();
  return session;
}

function getPage(session, params) {
  const pageId = requireString(params?.pageId, 'pageId', 128);
  const page = session.pages.get(pageId);
  if (!page) throw new SidecarError('PAGE_NOT_FOUND', 'browser page was not found');
  return { pageId, page };
}

function locatorFor(page, spec) {
  if (!spec || typeof spec !== 'object' || Array.isArray(spec)) {
    throw new SidecarError('INVALID_REQUEST', 'locator is required');
  }
  const exact = Boolean(spec.exact);
  const kinds = ['role', 'label', 'text', 'testId', 'css'].filter(key => typeof spec[key] === 'string' && spec[key].length > 0);
  if (kinds.length !== 1) throw new SidecarError('INVALID_REQUEST', 'locator must specify exactly one selector kind');
  for (const key of kinds) requireString(spec[key], `locator.${key}`, key === 'css' ? 1024 : 256);
  switch (kinds[0]) {
    case 'role': {
      const options = { exact };
      if (typeof spec.name === 'string' && spec.name.length > 0) options.name = requireString(spec.name, 'locator.name', 256);
      return page.getByRole(spec.role, options);
    }
    case 'label': return page.getByLabel(spec.label, { exact });
    case 'text': return page.getByText(spec.text, { exact });
    case 'testId': return page.getByTestId(spec.testId);
    case 'css': {
      if (spec.css.includes('>>') || /^\s*(?:xpath|text|id|data-testid|css)\s*=/.test(spec.css)) {
        throw new SidecarError('INVALID_REQUEST', 'css locator must contain a plain CSS selector');
      }
      return page.locator(`css=${spec.css}`);
    }
    default: throw new SidecarError('INVALID_REQUEST', 'unsupported locator');
  }
}

async function launch(params) {
  if (sessions.size >= MAX_SESSIONS) throw new SidecarError('BROWSER_BUSY', 'browser session limit reached', true);
  const browserSessionId = requireString(params?.browserSessionId, 'browserSessionId', 128);
  if (sessions.has(browserSessionId)) throw new SidecarError('CONFLICT', 'browser session already exists');
  const engine = params?.engine || 'chromium';
  if (engine !== 'chromium') throw new SidecarError('BROWSER_ENGINE_UNAVAILABLE', 'Phase 5 MVP currently supports chromium only');
  if (params?.allowedOrigins !== undefined && !Array.isArray(params.allowedOrigins)) {
    throw new SidecarError('INVALID_REQUEST', 'allowedOrigins must be an array');
  }
  if ((params?.allowedOrigins?.length || 0) > 32) {
    throw new SidecarError('INVALID_REQUEST', 'allowed origin limit exceeded');
  }
  const allowPrivateNetwork = params?.allowPrivateNetwork === true;
  const allowedOrigins = (params?.allowedOrigins || []).map(item => ({
    origin: requireString(item.origin, 'allowedOrigin.origin', 512),
    pinnedIps: Array.isArray(item.pinnedIps) ? item.pinnedIps.map(ip => requireString(ip, 'allowedOrigin.pinnedIp', 128)) : [],
  }));
  if (allowedOrigins.some(item => item.pinnedIps.length < 1 || item.pinnedIps.some(ip => net.isIP(ip) === 0))) {
    throw new SidecarError('INVALID_REQUEST', 'allowed origin policy is invalid');
  }
  for (const item of allowedOrigins) {
    if (normalizedOrigin(item.origin).origin !== item.origin) {
      throw new SidecarError('INVALID_REQUEST', 'allowed origin must use canonical form');
    }
  }
  const resolverRules = resolverRulesForOrigins(allowedOrigins);
  const width = Number(params?.viewport?.width ?? 1280);
  const height = Number(params?.viewport?.height ?? 720);
  if (!Number.isInteger(width) || !Number.isInteger(height) || width < 320 || height < 240 || width > 2560 || height > 1600) {
    throw new SidecarError('INVALID_REQUEST', 'viewport is outside the allowed range');
  }
  const screenshotDir = path.resolve(requireString(params?.screenshotDir, 'screenshotDir', 2048));
  await fs.mkdir(screenshotDir, { recursive: true });
  const browserArgs = ['--force-webrtc-ip-handling-policy=disable_non_proxied_udp'];
  if (resolverRules.length) browserArgs.push(`--host-resolver-rules=${resolverRules.join(',')}`);
  const browser = await chromium.launch({
    headless: params?.headless !== false,
    args: browserArgs,
  });
  const context = await browser.newContext({
    viewport: { width, height },
    acceptDownloads: false,
    serviceWorkers: 'block',
  });
  context.setDefaultTimeout(10_000);
  context.setDefaultNavigationTimeout(15_000);
  await context.addInitScript(() => {
    for (const name of ['RTCPeerConnection', 'webkitRTCPeerConnection', 'WebTransport']) {
      try {
        Object.defineProperty(globalThis, name, { value: undefined, configurable: false, writable: false });
      } catch {}
    }
  });
  const session = {
    id: browserSessionId,
    browser,
    context,
    pages: new Map(),
    allowedOrigins,
    allowPrivateNetwork,
    screenshotDir,
    events: [],
    eventSequence: 0,
    lastUsedAt: Date.now(),
  };
  try {
    await context.route('**/*', async route => {
      try {
        await assertRequestAllowed(session, route.request().url());
        await route.continue();
      } catch (error) {
        pushEvent(session, 'network_blocked', { url: safeEventURL(route.request().url()), reason: boundedText(error.message, 256) });
        await route.abort('blockedbyclient');
      }
    });
    await context.routeWebSocket('**/*', async ws => {
      try {
        await assertRequestAllowed(session, ws.url());
        ws.connectToServer();
      } catch (error) {
        pushEvent(session, 'websocket_blocked', { url: safeEventURL(ws.url()), reason: boundedText(error.message, 256) });
        await ws.close({ code: 1008, reason: 'blocked by Fast Spider network policy' });
      }
    });
    context.on('page', page => attachPage(session, page));
    sessions.set(browserSessionId, session);
    return { browserSessionId, engine: 'chromium', state: 'ready', viewport: { width, height } };
  } catch (error) {
    await context.close().catch(() => {});
    await browser.close().catch(() => {});
    throw error;
  }
}

async function closeSession(params) {
  const session = getSession(params);
  sessions.delete(session.id);
  await session.context.close().catch(() => {});
  await session.browser.close().catch(() => {});
  await fs.rm(session.screenshotDir, { recursive: true, force: true }).catch(() => {});
  return { browserSessionId: session.id, state: 'closed' };
}

async function pageOpen(params) {
  const session = getSession(params);
  if (session.pages.size >= MAX_PAGES) throw new SidecarError('BROWSER_LIMIT', 'page limit reached');
  const url = requireString(params?.url, 'url', 4096);
  await assertRequestAllowed(session, url);
  const page = await session.context.newPage();
  const pageId = attachPage(session, page);
  if (!pageId) throw new SidecarError('BROWSER_LIMIT', 'page limit reached');
  try {
    await page.goto(url, { waitUntil: waitUntilValue(params), timeout: actionTimeout(params) });
    return { pageId, url: page.url(), title: boundedText(await page.title(), 512) };
  } catch (error) {
    await page.close().catch(() => {});
    throw error;
  }
}

async function pageNavigate(params) {
  const session = getSession(params);
  const { pageId, page } = getPage(session, params);
  const url = requireString(params?.url, 'url', 4096);
  await assertRequestAllowed(session, url);
  await page.goto(url, { waitUntil: waitUntilValue(params), timeout: actionTimeout(params) });
  return { pageId, url: page.url(), title: boundedText(await page.title(), 512) };
}

async function pageClose(params) {
  const session = getSession(params);
  const { pageId, page } = getPage(session, params);
  await page.close();
  return { pageId, state: 'closed' };
}

async function pagesList(params) {
  const session = getSession(params);
  const pages = [];
  for (const [pageId, page] of session.pages) {
    if (page.isClosed()) continue;
    pages.push({ pageId, url: page.url(), title: boundedText(await page.title().catch(() => ''), 512) });
  }
  return { browserSessionId: session.id, pages };
}

async function locatorAction(action, params) {
  const session = getSession(params);
  const { pageId, page } = getPage(session, params);
  const locator = locatorFor(page, params.locator);
  const timeout = actionTimeout(params);
  switch (action) {
    case 'click':
      await locator.click({ timeout });
      break;
    case 'type':
      await locator.fill(requireString(params?.text, 'text', 16 * 1024), { timeout });
      break;
    case 'press':
      await locator.press(requireString(params?.key, 'key', 64), { timeout });
      break;
    case 'wait': {
      const state = params?.state || 'visible';
      if (!['attached', 'detached', 'visible', 'hidden'].includes(state)) throw new SidecarError('INVALID_REQUEST', 'wait state is invalid');
      await locator.waitFor({ state, timeout });
      break;
    }
    default:
      throw new SidecarError('INVALID_REQUEST', 'unsupported locator action');
  }
  return { pageId, url: page.url() };
}

async function snapshot(params) {
  const session = getSession(params);
  const { pageId, page } = getPage(session, params);
  const timeout = actionTimeout(params);
  const aria = await page.locator('body').ariaSnapshot({ timeout });
  const bounded = Buffer.byteLength(aria, 'utf8') <= MAX_SNAPSHOT_BYTES ? aria : `${Buffer.from(aria).subarray(0, MAX_SNAPSHOT_BYTES).toString('utf8')}\n# [truncated]`;
  return {
    pageId,
    url: page.url(),
    title: boundedText(await page.title(), 512),
    ariaSnapshot: bounded,
    truncated: bounded !== aria,
  };
}

async function screenshot(params) {
  const session = getSession(params);
  const { pageId, page } = getPage(session, params);
  const type = params?.format || 'png';
  if (type !== 'png' && type !== 'jpeg') throw new SidecarError('INVALID_REQUEST', 'screenshot format must be png or jpeg');
  const fullPage = Boolean(params?.fullPage);
  if (fullPage) {
    const dimensions = await page.evaluate(() => ({
      width: Math.max(document.documentElement.scrollWidth, document.body?.scrollWidth || 0),
      height: Math.max(document.documentElement.scrollHeight, document.body?.scrollHeight || 0),
    }));
    if (dimensions.width < 1 || dimensions.height < 1 || dimensions.width * dimensions.height > MAX_SCREENSHOT_PIXELS) {
      throw new SidecarError('SCREENSHOT_TOO_LARGE', 'full page exceeds screenshot pixel limit');
    }
  }
  const name = `shot_${crypto.randomUUID().replaceAll('-', '')}.${type === 'jpeg' ? 'jpg' : 'png'}`;
  const outputPath = path.join(session.screenshotDir, name);
  const options = { path: outputPath, type, fullPage, scale: 'css', timeout: actionTimeout(params) };
  if (type === 'jpeg') {
    const quality = Number(params?.quality ?? 80);
    if (!Number.isInteger(quality) || quality < 20 || quality > 95) throw new SidecarError('INVALID_REQUEST', 'jpeg quality must be between 20 and 95');
    options.quality = quality;
  }
  await page.screenshot(options);
  const info = await fs.stat(outputPath);
  if (!info.isFile() || info.size < 1 || info.size > MAX_SCREENSHOT_BYTES) {
    await fs.rm(outputPath, { force: true }).catch(() => {});
    throw new SidecarError('SCREENSHOT_TOO_LARGE', 'encoded screenshot exceeds size limit');
  }
  return { pageId, path: outputPath, logicalName: name, contentType: type === 'jpeg' ? 'image/jpeg' : 'image/png', sizeBytes: info.size };
}

function events(params) {
  const session = getSession(params);
  const cursor = Number(params?.cursor ?? 0);
  if (!Number.isInteger(cursor) || cursor < 0) throw new SidecarError('INVALID_REQUEST', 'event cursor is invalid');
  const values = session.events.filter(event => event.sequence > cursor).slice(0, 100);
  return { events: values, nextCursor: values.length ? values.at(-1).sequence : cursor, latestCursor: session.eventSequence };
}

async function dispatch(action, params = {}) {
  switch (action) {
    case 'runtime.status': return { protocolVersion: PROTOCOL_VERSION, runtime: 'playwright', browser: 'chromium', version: '1.62.0' };
    case 'launch': return launch(params);
    case 'close': return closeSession(params);
    case 'page.open': return pageOpen(params);
    case 'page.navigate': return pageNavigate(params);
    case 'page.close': return pageClose(params);
    case 'pages.list': return pagesList(params);
    case 'click': return locatorAction('click', params);
    case 'type': return locatorAction('type', params);
    case 'press': return locatorAction('press', params);
    case 'wait': return locatorAction('wait', params);
    case 'snapshot': return snapshot(params);
    case 'screenshot': return screenshot(params);
    case 'events': return events(params);
    default: throw new SidecarError('INVALID_REQUEST', 'unsupported browser action');
  }
}

async function closeAll() {
  const current = [...sessions.values()];
  sessions.clear();
  for (const session of current) {
    await session.context.close().catch(() => {});
    await session.browser.close().catch(() => {});
    await fs.rm(session.screenshotDir, { recursive: true, force: true }).catch(() => {});
  }
}

const rl = readline.createInterface({ input: process.stdin, crlfDelay: Infinity, terminal: false });
rl.on('line', async line => {
  let request;
  try {
    if (Buffer.byteLength(line, 'utf8') > 256 * 1024) throw new SidecarError('INVALID_REQUEST', 'sidecar request exceeds size limit');
    request = JSON.parse(line);
    const id = requireString(request?.id, 'id', 128);
    const action = requireString(request?.action, 'action', 64);
    response(id, await dispatch(action, request?.params || {}));
  } catch (error) {
    failure(request?.id || 'invalid', error);
  }
});

rl.on('close', async () => {
  await closeAll();
  process.exit(0);
});

for (const signal of ['SIGINT', 'SIGTERM']) {
  process.on(signal, async () => {
    await closeAll();
    process.exit(0);
  });
}
