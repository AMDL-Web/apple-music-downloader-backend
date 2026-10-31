// Settings › Hooks: the master switch, then each hook hooks.yaml defines
// with its runs over the stats window. A webhook is switched, tested, edited
// and deleted here (the sheet is settings-hook-sheet.js); a command hook is
// edited in hooks.yaml itself, which the API never writes. With no hooks,
// the presets (GET /hooks/presets) stand in for the list.

import { html, useEffect, useRef, useState } from '../ui/preact.js';
import { count, relTime } from '../lib/format.js';
import { STATS_DAYS } from '../lib/sysdata.js';
import { HOOK_EVENTS, typesLabel, webhookTarget, withEntry, withoutEntry } from '../lib/hook-presets.js';
import { setHooksEnabled, putHook, testHook, deleteHook } from '../api.js';
import { primeStatus, refreshStatus } from '../ui/system-status.js';
import { Button, Icon, useApp } from '../ui/components.js';
import { ConfirmDialog } from '../ui/dialogs.js';
import { MenuButton } from '../ui/menu.js';
import { HookSheet, PresetGrid, useHookPresets } from './settings-hook-sheet.js';

// How long a row says how its test went.
const TEST_SHOWN_MS = 10000;
// A test that answers sooner shows no "Sending" in between.
const TEST_SLOW_MS = 400;

const eventsLabel = (events) =>
  HOOK_EVENTS.filter((e) => (events || []).includes(e.id))
    .map((e) => e.label)
    .join(' · ');

/** A webhook's ⋯ menu: Edit…, Delete…. */
function RowMenu({ name, disabled, onEdit, onDelete }) {
  return html`<${MenuButton}
    label=${`More actions for ${name}`}
    heading=${name}
    size="sm"
    className="hook-more"
    disabled=${disabled}
    items=${[
      { id: 'edit', label: 'Edit…' },
      { id: 'delete', label: 'Delete…' },
    ]}
    onChoose=${(id) => (id === 'edit' ? onEdit() : onDelete())}
  />`;
}

/** The line under a webhook's name: how its last test went for a while, else what it calls. */
function HookSub({ entry, presets, test, now }) {
  let text = webhookTarget(entry, presets);
  let role;
  if (test && test.running && now - test.at >= TEST_SLOW_MS) text = 'Sending a test…';
  else if (test && !test.running && now - test.at < TEST_SHOWN_MS) {
    role = test.succeeded ? 'success' : 'danger';
    text = test.succeeded ? 'Test sent' : `Test failed: ${test.error || 'no answer'}`;
  }
  return html`<span class="settings-hook-sub mono" data-role=${role} title=${text} aria-live="polite">${text}</span>`;
}

function HookRow({ entry, presets, outcome, master, test, now, clock, offline, onSwitch, onTest, onEdit, onDelete }) {
  const command = entry.type !== 'webhook';
  const ok = (outcome && outcome.succeeded) || 0;
  const bad = (outcome && outcome.failed) || 0;
  const runs = ok + bad;
  const types = typesLabel(entry.job_types);
  return html`<li class="settings-hook" data-type=${entry.type} data-off=${entry.enabled && master ? undefined : ''}>
    <div class="settings-hook-head">
      <span class="settings-hook-name mono">${entry.name}</span>
      ${command ? html`<span class="settings-hook-chip">exec</span>` : null}
      ${command && !entry.enabled ? html`<span class="settings-hook-chip">Off</span>` : null}
    </div>
    ${command ? null : html`<${HookSub} entry=${entry} presets=${presets} test=${test} now=${clock} />`}
    <div class="settings-hook-scope">
      <span class="settings-hook-event">${eventsLabel(entry.events)}</span>
      ${types ? html`<span class="settings-hook-types">${types}</span>` : null}
    </div>
    <span class="settings-hook-runs">
      ${runs > 0
        ? html`<span class="settings-hook-counts" aria-label=${`${count(ok)} succeeded, ${count(bad)} failed`}>${count(ok)} ✓ · ${count(bad)} ✕</span>
            <span class="viz-split settings-hook-split" aria-hidden="true">
              <span class="viz-seg" data-tone="success" style=${{ '--w': String(ok / runs) }}></span>
              <span class="viz-seg" data-tone="danger" style=${{ '--w': String(bad / runs) }}></span>
            </span>
            ${outcome && outcome.last_failed_at ? html`<span class="settings-hook-last">${relTime(outcome.last_failed_at, now)}</span>` : null}`
        : html`<span class="settings-hook-none">No runs in ${STATS_DAYS} days</span>`}
    </span>
    <div class="settings-hook-controls">
      ${command
        ? html`<span class="settings-hook-file" title="Edited in hooks.yaml" aria-label="Edited in hooks.yaml"><${Icon} name="lock" size="sm" /><span class="mono" aria-hidden="true">hooks.yaml</span></span>`
        : html`<${Button}
              variant="ghost"
              size="sm"
              className="hook-test"
              aria-label=${`Send a test of ${entry.name}`}
              aria-disabled=${test && test.running ? 'true' : undefined}
              disabled=${offline}
              onClick=${test && test.running ? undefined : onTest}
            >Test</${Button}>
            <label class="hook-switch">
              <input type="checkbox" role="switch" class="field-switch" aria-label=${`${entry.name} on`} checked=${entry.enabled} disabled=${offline} onChange=${(e) => onSwitch(e.currentTarget.checked)} />
            </label>
            <${RowMenu} name=${entry.name} disabled=${offline} onEdit=${onEdit} onDelete=${onDelete} />`}
    </div>
  </li>`;
}

/**
 * The section's body below its fact rows. `hooks` is GET /hooks's answer
 * (undefined before it), `outcomes` /stats `hooks`, `now` the page clock,
 * `offline` whether writes are off.
 */
export function HooksPanel({ hooks, outcomes, now, offline }) {
  const app = useApp();
  const presets = useHookPresets();
  const [tests, setTests] = useState({});
  const [sheet, setSheet] = useState(null);
  const [confirm, setConfirm] = useState(null);
  const [, setTick] = useState(0);
  const latest = useRef(hooks);
  latest.current = hooks;
  // Redraw while a test is new enough to show, so it gives way on time.
  useEffect(() => {
    const live = Object.values(tests).some((t) => t.running || Date.now() - t.at < TEST_SHOWN_MS);
    if (!live) return undefined;
    const id = setInterval(() => setTick((n) => n + 1), 250);
    return () => clearInterval(id);
  }, [tests]);
  if (!hooks) return null;
  const entries = Array.isArray(hooks.entries) ? hooks.entries : [];
  const byName = new Map((outcomes || []).map((o) => [o.name, o]));
  // A test's time is measured against the wall clock.
  const clock = Date.now();

  const failed = (what, err) => {
    app.toast({ message: `${what}: ${(err && err.message) || 'the server refused it'}`, role: 'danger' });
    refreshStatus('hooks');
  };
  const setMaster = async (on) => {
    primeStatus('hooks', { ...latest.current, enabled: on });
    try {
      primeStatus('hooks', await setHooksEnabled(on));
    } catch (err) {
      failed('Hooks did not change', err);
    }
  };
  const setOne = async (entry, on) => {
    primeStatus('hooks', withEntry(latest.current, { ...entry, enabled: on }));
    try {
      primeStatus('hooks', withEntry(latest.current, await putHook(entry.name, { enabled: on })));
    } catch (err) {
      failed(`${entry.name} did not change`, err);
    }
  };
  const runTest = async (name) => {
    setTests((t) => ({ ...t, [name]: { running: true, at: Date.now() } }));
    let result;
    try {
      result = await testHook(name);
    } catch (err) {
      result = { succeeded: false, error: err.message };
    }
    setTests((t) => ({ ...t, [name]: { running: false, at: Date.now(), succeeded: Boolean(result && result.succeeded), error: result && result.error } }));
  };
  const remove = async () => {
    const name = confirm.name;
    setConfirm({ name, busy: true });
    try {
      await deleteHook(name);
      primeStatus('hooks', withoutEntry(latest.current, name));
      setTests(({ [name]: _gone, ...rest }) => rest);
      setConfirm(null);
      app.toast({ message: `Deleted ${name}` });
      // The row and its menu are gone; focus stays in the section.
      requestAnimationFrame(() => {
        const target = document.querySelector('.hooks-add') || document.querySelector('.hook-preset') || document.querySelector('.hooks-master input');
        if (target) target.focus({ preventScroll: true });
      });
    } catch (err) {
      setConfirm(null);
      failed(`${name} was not deleted`, err);
    }
  };
  const saved = (entry) => {
    primeStatus('hooks', withEntry(latest.current, entry));
    // A test of what the hook called before says nothing about it now.
    setTests(({ [entry.name]: _old, ...rest }) => rest);
    setSheet(null);
  };

  return html`<div class="hooks-panel">
    <div class="hooks-master">
      <label class="hooks-master-switch">
        <input type="checkbox" role="switch" class="field-switch" checked=${hooks.enabled} disabled=${offline} onChange=${(e) => setMaster(e.currentTarget.checked)} />
        <span>Run hooks</span>
      </label>
    </div>
    ${entries.length
      ? html`<ul class="settings-hooks">
          ${entries.map(
            (h) => html`<${HookRow}
              key=${h.name}
              entry=${h}
              presets=${presets.presets}
              outcome=${byName.get(h.name)}
              master=${hooks.enabled}
              test=${tests[h.name]}
              now=${now}
              clock=${clock}
              offline=${offline}
              onSwitch=${(on) => setOne(h, on)}
              onTest=${() => runTest(h.name)}
              onEdit=${() => setSheet({ editing: h })}
              onDelete=${() => setConfirm({ name: h.name })}
            />`,
          )}
        </ul>
        <div class="hooks-add-row">
          <${Button} icon="plus" className="hooks-add" disabled=${offline} onClick=${() => setSheet({ preset: null })}>Add hook</${Button}>
        </div>`
      : html`<${PresetGrid} presets=${presets.presets} error=${presets.error} onRetry=${presets.retry} disabled=${offline} onPick=${(p) => setSheet({ preset: p })} />`}
    ${sheet ? html`<${HookSheet} entries=${entries} editing=${sheet.editing} preset=${sheet.preset} presets=${presets} onSaved=${saved} onClose=${() => setSheet(null)} />` : null}
    ${confirm
      ? html`<${ConfirmDialog}
          title=${`Delete “${confirm.name}”?`}
          confirmLabel="Delete"
          busyLabel="Deleting…"
          danger
          busy=${Boolean(confirm.busy)}
          onConfirm=${remove}
          onCancel=${() => setConfirm(null)}
        >It leaves hooks.yaml and runs no more.</${ConfirmDialog}>`
      : null}
  </div>`;
}
