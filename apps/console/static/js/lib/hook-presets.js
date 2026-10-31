// The hooks editor's rules: the events and job types a hook runs on, how
// the sheet's choices combine, and the write it sends. The services (the
// presets) and the requests they make are the backend's: GET
// /api/v1/hooks/presets describes each one's fields, and PUT
// /api/v1/hooks/{name} takes {preset, values} and builds the request.
// Pure, so node --test covers it.

/** The events a hook runs on, in the editor's order. */
export const HOOK_EVENTS = [
  { id: 'job_finished', label: 'Finished' },
  { id: 'job_failed', label: 'Failed' },
  { id: 'job_cancelled', label: 'Cancelled' },
  { id: 'job_queued', label: 'Queued' },
];

/** The job types a hook can be limited to, in the editor's order. */
export const HOOK_TYPES = [
  { id: 'song', label: 'Songs' },
  { id: 'album', label: 'Albums' },
  { id: 'playlist', label: 'Playlists' },
  { id: 'artist', label: 'Artists' },
  { id: 'station', label: 'Stations' },
];

/** The preset kinds GET /hooks/presets names, in the editor's order, with their headings. */
export const PRESET_KINDS = [
  { id: 'notify', label: 'Notify' },
  { id: 'library', label: 'Refresh a library' },
  { id: 'custom', label: 'Other' },
];

/** The presets of GET /hooks/presets grouped by kind, kinds in the editor's order (an unknown kind goes last). */
export function presetGroups(presets) {
  const list = Array.isArray(presets) ? presets : [];
  const known = PRESET_KINDS.map((k) => ({ ...k, presets: list.filter((p) => p.kind === k.id) }));
  const other = list.filter((p) => !PRESET_KINDS.some((k) => k.id === p.kind));
  if (other.length) known[known.length - 1] = { ...known[known.length - 1], presets: [...known[known.length - 1].presets, ...other] };
  return known.filter((g) => g.presets.length);
}

/** The values a preset's fields start with: a choice field its default (else its first choice), the rest empty. */
export function presetValues(preset) {
  const out = {};
  for (const f of (preset && preset.fields) || []) {
    out[f.key] = Array.isArray(f.choices) && f.choices.length ? (f.choices.includes(f.default) ? f.default : f.choices[0]) : '';
  }
  return out;
}

/** The first required field left empty, as the backend would name it ("Topic is needed"), or ''. */
export function missingValue(preset, values) {
  for (const f of (preset && preset.fields) || []) {
    const v = values[f.key] === undefined || values[f.key] === null ? '' : String(values[f.key]);
    if (!f.optional && !f.default && (f.multiline ? v : v.trim()) === '') return `${f.label} is needed`;
  }
  return '';
}

/** The preset id a webhook was made from, as GET /hooks names it; 'custom' when it names none. */
export const entryPreset = (entry) => (entry && entry.preset) || 'custom';

/**
 * What a webhook calls, for the line under its name: "Plex · nas:32400", or
 * the host alone for a custom one or one named after its service. `presets`
 * gives the names; without them the host alone.
 */
export function webhookTarget(entry, presets) {
  const host = String(entry.origin || '').replace(/^https?:\/\//, '');
  const id = entryPreset(entry);
  const p = Array.isArray(presets) ? presets.find((x) => x.id === id) : null;
  if (!p || id === 'custom' || String(entry.name).toLowerCase() === p.name.toLowerCase()) return host;
  return `${p.name} · ${host}`;
}

/**
 * The events after a click on `id`: a queued download is a hook of its own
 * (the backend runs one entry once per download), so turning Queued on, or
 * anything while Queued is on, starts over with that one choice.
 */
export function toggleEvent(events, id) {
  const on = !events.includes(id);
  const next = (on && id === 'job_queued') || events.includes('job_queued') ? [] : events.filter((e) => e !== id);
  if (on) next.push(id);
  return HOOK_EVENTS.map((e) => e.id).filter((e) => next.includes(e));
}

/** The job types after a click on `id` ('all' clears them); all five is the same as none: every type. */
export function toggleType(types, id) {
  if (id === 'all') return [];
  const next = types.includes(id) ? types.filter((t) => t !== id) : [...types, id];
  const ordered = HOOK_TYPES.map((t) => t.id).filter((t) => next.includes(t));
  return ordered.length === HOOK_TYPES.length ? [] : ordered;
}

/** The job types a hook is limited to in words, "" for all of them. */
export function typesLabel(types) {
  const list = Array.isArray(types) ? types : [];
  if (list.length === 0 || HOOK_TYPES.every((t) => list.includes(t.id))) return '';
  return HOOK_TYPES.filter((t) => list.includes(t.id))
    .map((t) => t.label)
    .join(', ');
}

/** base, or base-2, base-3… when an entry has it. */
export function freeName(base, entries) {
  const taken = (n) => entries.some((e) => e.name === n);
  let name = base;
  for (let i = 2; taken(name); i++) name = `${base}-${i}`;
  return name;
}

/**
 * The WebhookWrite the sheet sends, or {error}. `draft`: {name, editing
 * (the entry's name, '' when adding), replace, preset (the descriptor
 * picked), values, events, types}. A new hook carries {preset, values} and
 * enabled; one pointed anew (replace) {preset, values}; an edit only its
 * events and job types, so the address and keys stay as they are.
 * job_types is always sent, [] for all.
 */
export function hookWrite(draft, entries) {
  let name = draft.editing;
  if (!name) {
    name = String(draft.name || '').trim();
    if (!name) return { error: 'Name is needed' };
    if (freeName(name, entries) !== name) return { error: `A hook is named ${name}` };
  }
  const events = HOOK_EVENTS.map((e) => e.id).filter((e) => draft.events.includes(e));
  if (events.length === 0) return { error: 'Pick when it runs' };
  const body = { events, job_types: HOOK_TYPES.map((t) => t.id).filter((t) => draft.types.includes(t)) };
  if (!draft.editing || draft.replace) {
    if (!draft.preset) return { error: 'Pick a service' };
    const missing = missingValue(draft.preset, draft.values);
    if (missing) return { error: missing };
    const values = {};
    for (const f of draft.preset.fields || []) {
      const v = draft.values[f.key] === undefined || draft.values[f.key] === null ? '' : String(draft.values[f.key]);
      values[f.key] = f.multiline ? v : v.trim();
    }
    Object.assign(body, { preset: draft.preset.id, values });
    if (!draft.editing) body.enabled = true;
  }
  return { name, body };
}

/** `hooks` (a HooksResponse) with `entry` in place of the one of its name, or appended. */
export function withEntry(hooks, entry) {
  const entries = hooks && Array.isArray(hooks.entries) ? hooks.entries : [];
  const at = entries.findIndex((e) => e.name === entry.name);
  const next = at < 0 ? [...entries, entry] : entries.map((e, i) => (i === at ? entry : e));
  return { enabled: Boolean(hooks && hooks.enabled), entries: next };
}

/** `hooks` without the entry named `name`. */
export function withoutEntry(hooks, name) {
  const entries = hooks && Array.isArray(hooks.entries) ? hooks.entries : [];
  return { enabled: Boolean(hooks && hooks.enabled), entries: entries.filter((e) => e.name !== name) };
}
