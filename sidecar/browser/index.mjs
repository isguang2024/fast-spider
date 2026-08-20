import crypto from 'node:crypto';
import fs from 'node:fs/promises';
import path from 'node:path';
import process from 'node:process';
import readline from 'node:readline';
import { chromium } from 'playwright';

const PROTOCOL_VERSION = '1.1';
const MAX_SESSIONS = 1;
const MAX_PAGES = 8;
const MAX_EVENTS = 200;
const MAX_EVENT_TEXT = 2048;
const MAX_SNAPSHOT_BYTES = 64 * 1024;
const MAX_SCREENSHOT_BYTES = 8 * 1024 * 1024;
const MAX_SCREENSHOT_PIXELS = 20_000_000;
const MAX_ACTION_TIMEOUT_MS = 30_000;
const MAX_ELEMENT_REFS = 512;
const MAX_BATCH_STEPS = 32;

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

function navigationURL(rawURL) {
  let parsed;
  try {
    parsed = new URL(rawURL);
  } catch {
    throw new SidecarError('BROWSER_NETWORK_DENIED', 'navigation URL is invalid');
  }
  if (!['http:', 'https:'].includes(parsed.protocol)) {
    throw new SidecarError('BROWSER_NETWORK_DENIED', 'navigation scheme is not allowed');
  }
  const authorityEnd = rawURL.search(/[/?#]/, rawURL.indexOf('//') + 2);
  const authority = rawURL.slice(rawURL.indexOf('//') + 2, authorityEnd < 0 ? rawURL.length : authorityEnd);
  if (parsed.username !== '' || parsed.password !== '' || authority.includes('@')) {
    throw new SidecarError('BROWSER_NETWORK_DENIED', 'navigation URL must not contain credentials');
  }
  return parsed.toString();
}

function pushEvent(session, type, data = {}) {
  session.eventSequence += 1;
  session.events.push({ sequence: session.eventSequence, type, timestamp: new Date().toISOString(), ...data });
  if (session.events.length > MAX_EVENTS) session.events.splice(0, session.events.length - MAX_EVENTS);
}

function clearPageRefs(session, pageId) {
  for (const [ref, entry] of session.refs) {
    if (entry.pageId !== pageId) continue;
    session.refs.delete(ref);
    void entry.handle.dispose().catch(() => {});
  }
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
  page.on('framenavigated', frame => {
    if (frame === page.mainFrame()) clearPageRefs(session, pageId);
  });
  page.on('close', () => {
    clearPageRefs(session, pageId);
    session.pages.delete(pageId);
  });
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

function staleRef(ref) {
  return new SidecarError('BROWSER_REF_STALE', `element ref ${ref} is stale; take a new snapshot`);
}

async function refEntryFor(session, pageId, rawRef) {
  const ref = requireString(rawRef, 'ref', 128);
  const entry = session.refs.get(ref);
  if (!entry || entry.pageId !== pageId) throw staleRef(ref);
  let connected = false;
  try {
    connected = await entry.handle.evaluate(element => Boolean(element?.isConnected));
  } catch {}
  if (!connected) {
    session.refs.delete(ref);
    void entry.handle.dispose().catch(() => {});
    throw staleRef(ref);
  }
  return { ref, handle: entry.handle };
}

function inferInteractiveMetadata(element) {
  const tag = element.tagName.toLowerCase();
  const explicitRole = (element.getAttribute('role') || '').trim();
  const inputType = tag === 'input' ? (element.getAttribute('type') || 'text').toLowerCase() : '';
  let role = explicitRole;
  if (!role) {
    if (tag === 'a' && element.hasAttribute('href')) role = 'link';
    else if (tag === 'button' || (tag === 'input' && ['button', 'submit', 'reset', 'image'].includes(inputType))) role = 'button';
    else if (tag === 'input' && inputType === 'checkbox') role = 'checkbox';
    else if (tag === 'input' && inputType === 'radio') role = 'radio';
    else if (tag === 'input' && inputType === 'range') role = 'slider';
    else if (tag === 'input' && inputType === 'number') role = 'spinbutton';
    else if (tag === 'input' || tag === 'textarea' || element.isContentEditable) role = 'textbox';
    else if (tag === 'select') role = element.multiple ? 'listbox' : 'combobox';
    else if (tag === 'summary') role = 'button';
    else role = tag;
  }
  const labelText = element.labels ? Array.from(element.labels).map(label => label.innerText || label.textContent || '').join(' ').trim() : '';
  const ariaLabel = (element.getAttribute('aria-label') || '').trim();
  const alt = (element.getAttribute('alt') || '').trim();
  const placeholder = (element.getAttribute('placeholder') || '').trim();
  const title = (element.getAttribute('title') || '').trim();
  const ownText = (element.innerText || element.textContent || '').trim();
  const name = ariaLabel || labelText || alt || placeholder || title || ownText;
  return {
    role,
    name,
    tag,
    inputType,
    disabled: element.matches(':disabled') || element.getAttribute('aria-disabled') === 'true',
    checked: typeof element.checked === 'boolean' ? element.checked : undefined,
  };
}

async function snapshotRefs(session, pageId, page) {
  clearPageRefs(session, pageId);
  const locator = page.locator('a[href],button,input,textarea,select,summary,[role],[contenteditable="true"],[tabindex]:not([tabindex="-1"])');
  const total = await locator.count();
  const count = Math.min(total, MAX_ELEMENT_REFS);
  const refs = [];
  for (let index = 0; index < count; index += 1) {
    const candidate = locator.nth(index);
    if (!(await candidate.isVisible().catch(() => false))) continue;
    const handle = await candidate.elementHandle({ timeout: 1000 }).catch(() => null);
    if (!handle) continue;
    let metadata;
    try {
      metadata = await handle.evaluate(inferInteractiveMetadata);
    } catch {
      await handle.dispose().catch(() => {});
      continue;
    }
    session.refSequence += 1;
    const ref = `e_${session.refSequence.toString(36)}`;
    session.refs.set(ref, { pageId, handle });
    refs.push({
      ref,
      role: boundedText(metadata.role, 64),
      name: boundedText(metadata.name, 256),
      tag: boundedText(metadata.tag, 32),
      inputType: boundedText(metadata.inputType, 32),
      disabled: Boolean(metadata.disabled),
      ...(typeof metadata.checked === 'boolean' ? { checked: metadata.checked } : {}),
    });
  }
  return { refs, truncated: total > MAX_ELEMENT_REFS };
}

function agentSnapshotText(refs) {
  return refs.map(item => {
    const escapedName = item.name ? ` "${item.name.replaceAll('"', '\\"')}"` : '';
    const flags = `${item.disabled ? ' [disabled]' : ''}${typeof item.checked === 'boolean' ? ` [checked=${item.checked}]` : ''}`;
    return `- ${item.role || item.tag}${escapedName} [ref=${item.ref}]${flags}`;
  }).join('\n');
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
  if (engine !== 'chromium') throw new SidecarError('BROWSER_ENGINE_UNAVAILABLE', 'managed browser currently supports chromium only');
  const width = Number(params?.viewport?.width ?? 1280);
  const height = Number(params?.viewport?.height ?? 720);
  if (!Number.isInteger(width) || !Number.isInteger(height) || width < 320 || height < 240 || width > 2560 || height > 1600) {
    throw new SidecarError('INVALID_REQUEST', 'viewport is outside the allowed range');
  }
  const screenshotDir = path.resolve(requireString(params?.screenshotDir, 'screenshotDir', 2048));
  await fs.mkdir(screenshotDir, { recursive: true });
  const headless = params?.headless !== false;
  let browser = null;
  let context = null;
  try {
    const browserArgs = ['--force-webrtc-ip-handling-policy=disable_non_proxied_udp'];
    browser = await chromium.launch({ headless, args: browserArgs });
    context = await browser.newContext({
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
  } catch (error) {
    await context?.close().catch(() => {});
    await browser?.close().catch(() => {});
    throw error;
  }
  const session = {
    id: browserSessionId,
    browser,
    context,
    pages: new Map(),
    refs: new Map(),
    refSequence: 0,
    screenshotDir,
    events: [],
    eventSequence: 0,
    lastUsedAt: Date.now(),
  };
  try {
    context.on('page', page => attachPage(session, page));
    sessions.set(browserSessionId, session);
    return { browserSessionId, engine: 'chromium', state: 'ready', viewport: { width, height } };
  } catch (error) {
    await context.close().catch(() => {});
    await browser?.close().catch(() => {});
    throw error;
  }
}

async function closeSession(params) {
  const session = getSession(params);
  sessions.delete(session.id);
  await session.context.close().catch(() => {});
  await session.browser?.close().catch(() => {});
  await fs.rm(session.screenshotDir, { recursive: true, force: true }).catch(() => {});
  return { browserSessionId: session.id, state: 'closed' };
}

async function pageOpen(params) {
  const session = getSession(params);
  if (session.pages.size >= MAX_PAGES) throw new SidecarError('BROWSER_LIMIT', 'page limit reached');
  const url = navigationURL(requireString(params?.url, 'url', 4096));
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
  const url = navigationURL(requireString(params?.url, 'url', 4096));
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

async function runRefAction(action, session, pageId, page, params, timeout) {
  const ref = requireString(params?.ref, 'ref', 128);
  const state = params?.state || 'visible';
  if (action === 'wait' && !['attached', 'detached', 'visible', 'hidden'].includes(state)) {
    throw new SidecarError('INVALID_REQUEST', 'wait state is invalid');
  }
  if (action === 'wait' && state === 'detached') {
    const entry = session.refs.get(ref);
    if (!entry || entry.pageId !== pageId) throw staleRef(ref);
    const deadline = Date.now() + timeout;
    while (Date.now() <= deadline) {
      let connected = false;
      try {
        connected = await entry.handle.evaluate(element => Boolean(element?.isConnected));
      } catch {
        connected = false;
      }
      if (!connected) {
        session.refs.delete(ref);
        void entry.handle.dispose().catch(() => {});
        return;
      }
      await new Promise(resolve => setTimeout(resolve, 50));
    }
    throw new SidecarError('BROWSER_TIMEOUT', `wait for element ref ${ref} to detach timed out`, true);
  }

  const entry = await refEntryFor(session, pageId, ref);
  try {
    switch (action) {
      case 'click':
        await entry.handle.click({ timeout });
        break;
      case 'type':
        await entry.handle.fill(requireString(params?.text, 'text', 16 * 1024), { timeout });
        break;
      case 'press':
        await entry.handle.press(requireString(params?.key, 'key', 64), { timeout });
        break;
      case 'wait':
        if (state === 'attached') return;
        await entry.handle.waitForElementState(state, { timeout });
        break;
      default:
        throw new SidecarError('INVALID_REQUEST', 'unsupported ref action');
    }
  } catch (error) {
    try {
      await refEntryFor(session, pageId, ref);
    } catch (refError) {
      if (refError instanceof SidecarError && refError.code === 'BROWSER_REF_STALE') throw refError;
    }
    throw error;
  }
}

async function locatorAction(action, params) {
  const session = getSession(params);
  const { pageId, page } = getPage(session, params);
  const hasRef = typeof params?.ref === 'string' && params.ref.length > 0;
  const hasLocator = Boolean(params?.locator && typeof params.locator === 'object' && !Array.isArray(params.locator));
  if (hasRef === hasLocator) throw new SidecarError('INVALID_REQUEST', 'provide exactly one of ref or locator');
  const timeout = actionTimeout(params);
  if (hasRef) {
    await runRefAction(action, session, pageId, page, params, timeout);
    return { pageId, url: page.url(), ref: params.ref };
  }

  const locator = locatorFor(page, params.locator);
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
  const refResult = await snapshotRefs(session, pageId, page);
  const bounded = Buffer.byteLength(aria, 'utf8') <= MAX_SNAPSHOT_BYTES ? aria : `${Buffer.from(aria).subarray(0, MAX_SNAPSHOT_BYTES).toString('utf8')}\n# [truncated]`;
  return {
    pageId,
    url: page.url(),
    title: boundedText(await page.title(), 512),
    ariaSnapshot: bounded,
    agentSnapshot: agentSnapshotText(refResult.refs),
    refs: refResult.refs,
    refCount: refResult.refs.length,
    refsTruncated: refResult.truncated,
    truncated: bounded !== aria,
  };
}

async function batch(params) {
  const session = getSession(params);
  const { pageId, page } = getPage(session, params);
  const steps = params?.steps;
  if (!Array.isArray(steps) || steps.length < 1 || steps.length > MAX_BATCH_STEPS) {
    throw new SidecarError('INVALID_REQUEST', `batch steps must contain 1-${MAX_BATCH_STEPS} actions`);
  }
  let completedSteps = 0;
  for (let index = 0; index < steps.length; index += 1) {
    const step = steps[index];
    if (!step || typeof step !== 'object' || Array.isArray(step)) throw new SidecarError('INVALID_REQUEST', `batch step ${index + 1} is invalid`);
    const action = requireString(step.action, `steps[${index}].action`, 32);
    if (!['click', 'type', 'press', 'wait'].includes(action)) throw new SidecarError('INVALID_REQUEST', `batch step ${index + 1} action is not allowed`);
    const stepParams = {
      browserSessionId: session.id,
      pageId,
      ...(typeof step.ref === 'string' ? { ref: step.ref } : {}),
      ...(step.locator && typeof step.locator === 'object' && !Array.isArray(step.locator) ? { locator: step.locator } : {}),
      ...(typeof step.text === 'string' ? { text: step.text } : {}),
      ...(typeof step.key === 'string' ? { key: step.key } : {}),
      ...(typeof step.state === 'string' ? { state: step.state } : {}),
      timeoutMs: Number.isFinite(Number(step.timeoutMs)) ? Number(step.timeoutMs) : actionTimeout(params),
    };
    try {
      await locatorAction(action, stepParams);
      completedSteps += 1;
    } catch (error) {
      if (error instanceof SidecarError) {
        throw new SidecarError(error.code, `batch step ${index + 1} failed: ${error.message}`, error.retryable);
      }
      throw error;
    }
  }
  const result = { pageId, url: page.url(), completedSteps };
  if (params?.snapshotAfter === true) {
    result.snapshot = await snapshot({ browserSessionId: session.id, pageId, timeoutMs: actionTimeout(params) });
  }
  return result;
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
    case 'batch': return batch(params);
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
    await session.browser?.close().catch(() => {});
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
