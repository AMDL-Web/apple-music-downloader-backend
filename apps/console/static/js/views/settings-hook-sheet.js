// The sheet that adds a webhook or edits one (Settings › Hooks): pick a
// service, fill in what it asks for, then when the hook runs and for which
// downloads. The services and their fields come from GET
// /api/v1/hooks/presets, and the backend builds each request from {preset,
// values}. Editing changes the events and job types without sending the
// address and keys again; Replace… points the hook at a service anew.

import { html, useEffect, useRef, useState } from '../ui/preact.js';
import { HOOK_EVENTS, HOOK_TYPES, entryPreset, freeName, hookWrite, presetGroups, presetValues, toggleEvent, toggleType } from '../lib/hook-presets.js';
import { getHookPresets, putHook } from '../api.js';
import { Button, ErrorNote, Segmented } from '../ui/components.js';
import { Dialog } from '../ui/dialogs.js';
import { useMedia } from '../ui/hooks.js';

// The presets are the server's and do not change while it runs: one read per tab.
let presetsCache = null;

/** GET /hooks/presets once per tab: {presets, error, retry}; `presets` undefined until it answers. */
export function useHookPresets() {
  const [state, setState] = useState(() => (presetsCache ? { presets: presetsCache } : {}));
  const [attempt, setAttempt] = useState(0);
  useEffect(() => {
    if (presetsCache) return undefined;
    let live = true;
    getHookPresets().then(
      (res) => {
        presetsCache = res && Array.isArray(res.presets) ? res.presets : [];
        if (live) setState({ presets: presetsCache });
      },
      (error) => live && setState({ error }),
    );
    return () => {
      live = false;
    };
  }, [attempt]);
  return { ...state, retry: () => (setState({}), setAttempt((n) => n + 1)) };
}

/** The services a new hook can call, by kind; `onPick(preset)`. `scope` keeps its ids apart from another grid's. */
export function PresetGrid({ presets, error, onRetry, onPick, disabled, scope = 'page' }) {
  if (error) return html`<${ErrorNote} title="Couldn't load the services" error=${error} onRetry=${onRetry} />`;
  if (!presets) return null;
  return html`<div class="hook-presets">
    ${presetGroups(presets).map(
      (g) => html`<div key=${g.id} class="hook-preset-group" role="group" aria-labelledby=${`hook-kind-${scope}-${g.id}`}>
        <span class="hook-preset-label" id=${`hook-kind-${scope}-${g.id}`}>${g.label}</span>
        <div class="hook-preset-grid">
          ${g.presets.map(
            (p) => html`<${Button} key=${p.id} className="hook-preset" data-preset=${p.id} disabled=${disabled} onClick=${() => onPick(p)}>${p.name}</${Button}>`,
          )}
        </div>
      </div>`,
    )}
  </div>`;
}

function PresetField({ preset, field, value, onChange }) {
  const id = `hook-${preset.id}-${field.key}`;
  const placeholder = field.placeholder || field.default || '';
  if (Array.isArray(field.choices) && field.choices.length) {
    return html`<div class="form-row">
      <span class="field-label" id=${`${id}-label`}>${field.label}</span>
      <${Segmented} label=${field.label} options=${field.choices.map((c) => ({ value: c, label: c }))} value=${value} onChange=${onChange} />
    </div>`;
  }
  const common = {
    id,
    class: field.multiline ? 'field-input mono hook-body' : 'field-input',
    placeholder,
    value,
    autocomplete: field.secret ? 'new-password' : 'off',
    autocapitalize: 'off',
    spellcheck: false,
    onInput: (e) => onChange(e.currentTarget.value),
  };
  return html`<div class="form-row">
    <label class="field-label" for=${id}>${field.label}</label>
    ${field.multiline ? html`<textarea ...${common} rows="4"></textarea>` : html`<input ...${common} type=${field.secret ? 'password' : 'text'} />`}
  </div>`;
}

function CheckSet({ id, label, options, isOn, onToggle }) {
  return html`<div class="form-row">
    <span class="field-label" id=${id}>${label}</span>
    <div class="check-group" role="group" aria-labelledby=${id}>
      ${options.map(
        (o) => html`<label key=${o.id} class="check" data-choice=${o.id}>
          <input type="checkbox" checked=${isOn(o.id)} onChange=${() => onToggle(o.id)} />
          <span>${o.label}</span>
        </label>`,
      )}
    </div>
  </div>`;
}

/** `d` showing `preset`'s fields; a new hook also takes its name and events. */
function choose(d, preset, entries, editing) {
  const next = { ...d, preset, values: presetValues(preset) };
  if (editing) return next;
  return { ...next, name: freeName(preset.id, entries), events: [...(preset.events || [])] };
}

/**
 * Props: entries (GET /hooks's), editing (the entry edited, or undefined
 * when adding), preset (the service of a new hook, null to pick one),
 * presets (useHookPresets' state), onSaved(entry as written), onClose().
 */
export function HookSheet({ entries, editing, preset: startPreset, presets, onSaved, onClose }) {
  const phone = useMedia('(max-width: 639px)');
  const [d, setD] = useState(() => {
    const base = { name: '', preset: null, values: {}, replace: false, events: [], types: [] };
    if (editing) return { ...base, events: editing.events || [], types: editing.job_types || [] };
    return startPreset ? choose(base, startPreset, entries, false) : base;
  });
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState('');
  const body = useRef(null);
  const picking = !d.preset && (!editing || d.replace);
  // A new step starts at its title, as the sheet does: the next Tab reaches
  // its first control, and a phone's keyboard stays down until a field is tapped.
  const step = picking ? 'pick' : d.preset ? d.preset.id : 'edit';
  const first = useRef(true);
  useEffect(() => {
    if (first.current) {
      first.current = false;
      return;
    }
    const title = document.getElementById('hook-sheet-title');
    if (title) title.focus({ preventScroll: true });
    if (body.current) body.current.scrollTop = 0;
  }, [step]);

  const set = (patch) => {
    setErr('');
    setD((x) => ({ ...x, ...patch }));
  };
  const pick = (p) => {
    setErr('');
    setD((x) => choose(x, p, entries, Boolean(editing)));
  };
  const back = () => set(picking ? { replace: false } : { preset: null });
  const close = busy ? undefined : onClose;
  const save = async () => {
    if (busy) return;
    const w = hookWrite({ ...d, editing: editing ? editing.name : '' }, entries);
    if (w.error) {
      setErr(w.error);
      return;
    }
    setBusy(true);
    try {
      const entry = await putHook(w.name, w.body);
      onSaved(entry);
    } catch (e) {
      setBusy(false);
      setErr(e.message || 'The server refused it');
    }
  };

  const p = d.preset;
  const title = editing ? editing.name : p ? p.name : 'Add hook';
  const showBack = (p && (!editing || d.replace)) || (picking && d.replace);
  const known = editing && presets.presets ? presets.presets.find((x) => x.id === entryPreset(editing)) : null;
  const host = editing ? String(editing.origin || '').replace(/^https?:\/\//, '') : '';
  return html`<${Dialog} name="hook" className="sheet sheet-hook" placement=${phone ? 'bottom' : 'center'} labelledBy="hook-sheet-title" onClose=${close}>
    <div class="sheet-head hook-sheet-head">
      ${showBack ? html`<${Button} variant="ghost" size="sm" icon="chevron-left" className="hook-back" aria-label="Back" onClick=${back} />` : null}
      <h2 id="hook-sheet-title" class="card-title hook-sheet-title" tabindex="-1" data-autofocus>${title}</h2>
      <${Button} variant="ghost" size="sm" icon="x" aria-label="Close" onClick=${close} />
    </div>
    <div class="hook-sheet-body" ref=${body}>
      ${picking
        ? html`<${PresetGrid} scope="sheet" presets=${presets.presets} error=${presets.error} onRetry=${presets.retry} onPick=${pick} />`
        : html`<form class="hook-form" onSubmit=${(e) => (e.preventDefault(), save())}>
            ${editing
              ? null
              : html`<div class="form-row">
                  <label class="field-label" for="hook-name">Name</label>
                  <input id="hook-name" class="field-input mono" autocomplete="off" autocapitalize="off" spellcheck="false" value=${d.name} onInput=${(e) => set({ name: e.currentTarget.value })} />
                </div>`}
            ${editing && !d.replace
              ? html`<div class="hook-target">
                  <span class="hook-target-text mono">${known && known.id !== 'custom' ? `${known.name} · ${host}` : host}</span>
                  <${Button} variant="ghost" size="sm" className="hook-replace" onClick=${() => set({ replace: true, preset: null })}>Replace…</${Button}>
                </div>`
              : (p.fields || []).map(
                  (f) => html`<${PresetField} key=${`${p.id}-${f.key}`} preset=${p} field=${f} value=${d.values[f.key]} onChange=${(v) => set({ values: { ...d.values, [f.key]: v } })} />`,
                )}
            <${CheckSet} id="hook-events-label" label="Runs when a download" options=${HOOK_EVENTS} isOn=${(id) => d.events.includes(id)} onToggle=${(id) => set({ events: toggleEvent(d.events, id) })} />
            <${CheckSet}
              id="hook-types-label"
              label="For"
              options=${[{ id: 'all', label: 'All' }, ...HOOK_TYPES]}
              isOn=${(id) => (id === 'all' ? d.types.length === 0 : d.types.includes(id))}
              onToggle=${(id) => set({ types: toggleType(d.types, id) })}
            />
            <button type="submit" hidden></button>
          </form>`}
    </div>
    <div class="dialog-actions hook-sheet-actions">
      <p class="hook-sheet-error" role="alert">${err}</p>
      <${Button} className="hook-cancel" aria-disabled=${busy ? 'true' : undefined} onClick=${close}>Cancel</${Button}>
      ${picking
        ? null
        : html`<${Button} variant="primary" className="hook-save" aria-disabled=${busy ? 'true' : undefined} aria-busy=${busy ? 'true' : undefined} onClick=${save}>${
            busy ? (editing ? 'Saving…' : 'Adding…') : editing ? 'Save' : 'Add'
          }</${Button}>`}
    </div>
  </${Dialog}>`;
}
