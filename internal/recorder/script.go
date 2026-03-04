package recorder

import "fmt"

const recorderBindingName = "__bbaasRecorderSend"

func buildRecorderScript(options StartRecordingOptions) string {
	includeScroll := options.IncludeScroll != nil && *options.IncludeScroll
	includeKeys := options.IncludeKeys != nil && *options.IncludeKeys

	return fmt.Sprintf(`(() => {
  const BINDING_NAME = %q;
  const REDACTION_MODE = %q;
  const INCLUDE_SCROLL = %t;
  const INCLUDE_KEYS = %t;

  if (window.__bbaasRecorderBootstrapInstalled) {
    if (typeof window.__bbaasInstallRecorder === 'function') {
      window.__bbaasInstallRecorder();
    }
    return;
  }

  window.__bbaasRecorderBootstrapInstalled = true;

  const ACTIONABLE_SELECTOR = [
    'a[href]',
    'button',
    'input',
    'select',
    'textarea',
    'label',
    'summary',
    '[role="button"]',
    '[role="link"]',
    '[role="checkbox"]',
    '[role="radio"]',
    '[role="tab"]',
    '[role="menuitem"]',
    '[contenteditable=""]',
    '[contenteditable="true"]',
    '[tabindex]'
  ].join(',');

  const state = {
    installed: false,
    lastScrollAt: 0,
  };

  function send(action) {
    try {
      if (typeof window[BINDING_NAME] !== 'function') {
        return;
      }

      window[BINDING_NAME](JSON.stringify(action));
    } catch (_err) {
      // Ignore recorder transport failures in page context.
    }
  }

  function normalizeText(input) {
    if (!input) {
      return '';
    }

    return String(input).replace(/\s+/g, ' ').trim();
  }

  function isVisible(element) {
    if (!(element instanceof Element)) {
      return false;
    }

    const rect = element.getBoundingClientRect();
    const style = window.getComputedStyle(element);

    return style.visibility !== 'hidden' && style.display !== 'none' && rect.width > 0 && rect.height > 0;
  }

  function isActionable(element) {
    if (!(element instanceof Element)) {
      return false;
    }

    if (element.matches(ACTIONABLE_SELECTOR)) {
      if (element.hasAttribute('tabindex')) {
        const tabIndex = Number(element.getAttribute('tabindex'));
        if (!Number.isNaN(tabIndex) && tabIndex < 0) {
          return false;
        }
      }
      return true;
    }

    if (typeof element.onclick === 'function' || element.hasAttribute('onclick')) {
      return true;
    }

    return false;
  }

  function roleForElement(element) {
    if (!(element instanceof Element)) {
      return '';
    }

    const explicit = normalizeText(element.getAttribute('role'));
    if (explicit) {
      return explicit;
    }

    const tag = element.tagName.toLowerCase();
    if (tag === 'a' && element.hasAttribute('href')) {
      return 'link';
    }
    if (tag === 'button') {
      return 'button';
    }
    if (tag === 'input') {
      const inputType = normalizeText(element.getAttribute('type')).toLowerCase();
      if (inputType === 'checkbox') {
        return 'checkbox';
      }
      if (inputType === 'radio') {
        return 'radio';
      }
      if (inputType === 'submit' || inputType === 'button') {
        return 'button';
      }
      return 'textbox';
    }
    if (tag === 'select') {
      return 'combobox';
    }
    if (tag === 'textarea') {
      return 'textbox';
    }

    return '';
  }

  function accessibleName(element) {
    if (!(element instanceof Element)) {
      return '';
    }

    const ariaLabel = normalizeText(element.getAttribute('aria-label'));
    if (ariaLabel) {
      return ariaLabel;
    }

    const labelledBy = normalizeText(element.getAttribute('aria-labelledby'));
    if (labelledBy) {
      const labels = labelledBy
        .split(/\s+/)
        .map((id) => document.getElementById(id))
        .filter(Boolean)
        .map((node) => normalizeText(node.textContent))
        .filter(Boolean);

      if (labels.length > 0) {
        return labels.join(' ');
      }
    }

    if ('labels' in element && element.labels && element.labels.length > 0) {
      const labelText = Array.from(element.labels)
        .map((label) => normalizeText(label.textContent))
        .filter(Boolean)
        .join(' ');
      if (labelText) {
        return labelText;
      }
    }

    const directText = normalizeText(element.textContent);
    if (directText) {
      return directText.slice(0, 160);
    }

    return '';
  }

  function cssEscape(value) {
    if (window.CSS && typeof window.CSS.escape === 'function') {
      return window.CSS.escape(value);
    }

    return value.replace(/([#.;:[\],>+~*^$|=(){}\\])/g, '\\\\$1');
  }

  function cssPath(element) {
    if (!(element instanceof Element)) {
      return '';
    }

    if (element.id) {
      return '#' + cssEscape(element.id);
    }

    const segments = [];
    let current = element;

    while (current && current.nodeType === Node.ELEMENT_NODE && segments.length < 8) {
      let segment = current.tagName.toLowerCase();
      if (current.id) {
        segment += '#' + cssEscape(current.id);
        segments.unshift(segment);
        break;
      }

      const parent = current.parentElement;
      if (!parent) {
        segments.unshift(segment);
        break;
      }

      const siblings = Array.from(parent.children).filter((child) => child.tagName === current.tagName);
      if (siblings.length > 1) {
        const index = siblings.indexOf(current) + 1;
        segment += ':nth-of-type(' + index + ')';
      }

      segments.unshift(segment);
      current = parent;
    }

    return segments.join(' > ');
  }

  function xpathPath(element) {
    if (!(element instanceof Element)) {
      return '';
    }

    if (element.id) {
      return '//*[@id=' + JSON.stringify(element.id) + ']';
    }

    const parts = [];
    let current = element;

    while (current && current.nodeType === Node.ELEMENT_NODE) {
      let index = 1;
      let sibling = current.previousElementSibling;
      while (sibling) {
        if (sibling.tagName === current.tagName) {
          index += 1;
        }
        sibling = sibling.previousElementSibling;
      }

      parts.unshift(current.tagName.toLowerCase() + '[' + index + ']');
      current = current.parentElement;
    }

    return '/' + parts.join('/');
  }

  function domPath(element) {
    if (!(element instanceof Element)) {
      return '';
    }

    const segments = [];
    let current = element;

    while (current && current.nodeType === Node.ELEMENT_NODE && segments.length < 12) {
      const parent = current.parentElement;
      if (!parent) {
        segments.unshift(current.tagName.toLowerCase());
        break;
      }

      const siblings = Array.from(parent.children);
      const index = siblings.indexOf(current) + 1;
      segments.unshift(current.tagName.toLowerCase() + ':nth-child(' + index + ')');
      current = parent;
    }

    return segments.join(' > ');
  }

  function nodeSummary(node) {
    if (!(node instanceof Element)) {
      return null;
    }

    const classes = (node.className && typeof node.className === 'string')
      ? node.className.split(/\s+/).filter(Boolean).slice(0, 3)
      : [];

    return {
      tag: node.tagName.toLowerCase(),
      id: node.id || '',
      classes,
      role: roleForElement(node),
    };
  }

  function buildLocators(element) {
    if (!(element instanceof Element)) {
      return [];
    }

    const locators = [];

    const css = cssPath(element);
    if (css) {
      locators.push({ kind: 'css', value: css });
    }

    const xpath = xpathPath(element);
    if (xpath) {
      locators.push({ kind: 'xpath', value: xpath });
    }

    const role = roleForElement(element);
    const name = accessibleName(element);
    if (role) {
      locators.push({ kind: 'role', role, name });
    }

    const text = normalizeText(element.textContent).slice(0, 120);
    if (text) {
      locators.push({ kind: 'text', match: 'contains', value: text });
    }

    const path = domPath(element);
    if (path) {
      locators.push({ kind: 'dompath', value: path });
    }

    return locators;
  }

  function buildFingerprint(element) {
    if (!(element instanceof Element)) {
      return {};
    }

    const rect = element.getBoundingClientRect();
    const parentText = element.parentElement ? normalizeText(element.parentElement.textContent) : '';

    return {
      role: roleForElement(element),
      accessibleName: accessibleName(element),
      attrs: {
        id: element.id || '',
        name: element.getAttribute('name') || '',
        type: element.getAttribute('type') || '',
        ariaLabel: element.getAttribute('aria-label') || '',
        placeholder: element.getAttribute('placeholder') || '',
        dataTestID: element.getAttribute('data-testid') || '',
      },
      nearbyText: parentText.slice(0, 180),
      visibility: isVisible(element),
      rect: {
        x: Math.round(rect.x),
        y: Math.round(rect.y),
        w: Math.round(rect.width),
        h: Math.round(rect.height),
      },
    };
  }

  function buildResolvedTarget(element) {
    if (!(element instanceof Element)) {
      return {};
    }

    return {
      tag: element.tagName.toLowerCase(),
      role: roleForElement(element),
      type: element.getAttribute('type') || '',
      name: element.getAttribute('name') || '',
      id: element.id || '',
      ariaLabel: element.getAttribute('aria-label') || '',
    };
  }

  function findResolvedTarget(event) {
    const path = typeof event.composedPath === 'function' ? event.composedPath() : [];

    for (const candidate of path) {
      if (candidate instanceof Element && isActionable(candidate)) {
        return { target: candidate, path };
      }
    }

    const fallback = event.target instanceof Element ? event.target.closest(ACTIONABLE_SELECTOR) || event.target : null;
    return { target: fallback, path };
  }

  function redactText(element, value) {
    const textValue = value == null ? '' : String(value);

    if (REDACTION_MODE === 'off') {
      return textValue;
    }

    const inputType = element && element.getAttribute ? normalizeText(element.getAttribute('type')).toLowerCase() : '';
    if (inputType === 'password') {
      return '[REDACTED_PASSWORD]';
    }

    if (REDACTION_MODE === 'maskAllText') {
      if (!textValue) {
        return textValue;
      }
      return '[REDACTED_TEXT]';
    }

    if (REDACTION_MODE === 'maskInputs') {
      const tag = element instanceof Element ? element.tagName.toLowerCase() : '';
      if (tag === 'input' || tag === 'textarea' || element && element.isContentEditable) {
        if (!textValue) {
          return textValue;
        }
        return '[REDACTED_INPUT]';
      }
    }

    return textValue;
  }

  function baseAction(type, frameURL) {
    return {
      type,
      tsMonotonicMs: Number(window.performance && window.performance.now ? window.performance.now() : Date.now()),
      tsWallIso: new Date().toISOString(),
      frameUrl: frameURL || window.location.href,
      payload: {},
    };
  }

  function captureElementAction(type, event, buildPayload) {
    const resolved = findResolvedTarget(event);
    if (!resolved.target) {
      return;
    }

    const action = baseAction(type, window.location.href);
    const target = resolved.target;

    const pathSummary = (resolved.path || [])
      .filter((node) => node instanceof Element)
      .slice(0, 6)
      .map((node) => nodeSummary(node))
      .filter(Boolean);

    action.payload = {
      resolvedTarget: buildResolvedTarget(target),
      locators: buildLocators(target),
      fingerprint: buildFingerprint(target),
      composedPath: pathSummary,
      ...(typeof buildPayload === 'function' ? buildPayload(target, event) : {}),
    };

    const label = target.tagName === 'LABEL' ? target : (target.closest ? target.closest('label') : null);
    if (label instanceof HTMLLabelElement) {
      action.payload.targetKind = 'label';
      if (label.control) {
        action.payload.labelControl = {
          resolvedTarget: buildResolvedTarget(label.control),
          locators: buildLocators(label.control),
          fingerprint: buildFingerprint(label.control),
        };
      }
    }

    send(action);
  }

  function onClick(event) {
    captureElementAction('click', event, (_target, sourceEvent) => ({
      pointer: {
        button: sourceEvent.button,
        clickCount: sourceEvent.detail || 1,
        clientX: sourceEvent.clientX,
        clientY: sourceEvent.clientY,
      },
    }));
  }

  function onDoubleClick(event) {
    captureElementAction('dblclick', event, (_target, sourceEvent) => ({
      pointer: {
        button: sourceEvent.button,
        clickCount: sourceEvent.detail || 2,
        clientX: sourceEvent.clientX,
        clientY: sourceEvent.clientY,
      },
    }));
  }

  function onInput(event) {
    captureElementAction('type', event, (target, sourceEvent) => {
      const value = target && 'value' in target ? target.value : normalizeText(target.textContent);
      return {
        value: redactText(target, value),
        inputType: sourceEvent.inputType || '',
        selection: (typeof target.selectionStart === 'number' && typeof target.selectionEnd === 'number')
          ? { start: target.selectionStart, end: target.selectionEnd }
          : null,
      };
    });
  }

  function onChange(event) {
    captureElementAction('change', event, (target) => {
      const value = target && 'value' in target ? target.value : normalizeText(target.textContent);
      return { value: redactText(target, value) };
    });
  }

  function onSubmit(event) {
    captureElementAction('submit', event);
  }

  function onFocus(event) {
    captureElementAction('focus', event);
  }

  function onBlur(event) {
    captureElementAction('blur', event);
  }

  function onKeyDown(event) {
    captureElementAction('keydown', event, () => ({
      key: event.key,
      code: event.code,
      altKey: !!event.altKey,
      ctrlKey: !!event.ctrlKey,
      metaKey: !!event.metaKey,
      shiftKey: !!event.shiftKey,
    }));
  }

  function onKeyUp(event) {
    captureElementAction('keyup', event, () => ({
      key: event.key,
      code: event.code,
      altKey: !!event.altKey,
      ctrlKey: !!event.ctrlKey,
      metaKey: !!event.metaKey,
      shiftKey: !!event.shiftKey,
    }));
  }

  function onScroll(event) {
    const now = Date.now();
    if ((now - state.lastScrollAt) < 200) {
      return;
    }
    state.lastScrollAt = now;

    const target = event.target === document ? document.documentElement : event.target;
    if (!(target instanceof Element) && target !== window && target !== document) {
      return;
    }

    const action = baseAction('scroll', window.location.href);
    action.payload = {
      scrollX: window.scrollX,
      scrollY: window.scrollY,
      targetTag: target instanceof Element ? target.tagName.toLowerCase() : 'window',
    };

    send(action);
  }

  function onNavigate() {
    const action = baseAction('navigate', window.location.href);
    action.payload = {
      url: window.location.href,
      title: document.title || '',
    };
    send(action);
  }

  window.__bbaasInstallRecorder = function installRecorder() {
    if (state.installed) {
      return;
    }

    state.installed = true;

    document.addEventListener('click', onClick, true);
    document.addEventListener('dblclick', onDoubleClick, true);
    document.addEventListener('input', onInput, true);
    document.addEventListener('change', onChange, true);
    document.addEventListener('submit', onSubmit, true);
    document.addEventListener('focus', onFocus, true);
    document.addEventListener('blur', onBlur, true);

    if (INCLUDE_KEYS) {
      document.addEventListener('keydown', onKeyDown, true);
      document.addEventListener('keyup', onKeyUp, true);
    }

    if (INCLUDE_SCROLL) {
      document.addEventListener('scroll', onScroll, true);
      window.addEventListener('scroll', onScroll, true);
    }

    window.addEventListener('popstate', onNavigate, true);
    window.addEventListener('hashchange', onNavigate, true);
  };

  window.__bbaasInstallRecorder();
})();`, recorderBindingName, string(options.RedactionMode), includeScroll, includeKeys)
}
