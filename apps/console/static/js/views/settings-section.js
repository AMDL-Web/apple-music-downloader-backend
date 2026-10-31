// The open Settings section's body, below its status head: its draft
// fields, its fact rows (startup values) and its action blocks, never
// mixed. Server has sub-groups under headings (lib/settings-sections.js
// SETTINGS_GROUPS); a ?section= that names a group opens Server scrolled
// to it.

import { html } from '../ui/preact.js';
import { sectionFields, fieldDisabled } from '../lib/settings.js';
import { SETTINGS_GROUPS } from '../lib/settings-sections.js';
import { bytes, absTime } from '../lib/format.js';
import { groupByDisk } from '../lib/storage.js';
import { settingsHash } from '../lib/route.js';
import { tokenState } from '../lib/user-token.js';
import { Notice, ErrorNote, SkeletonRows, Unavailable, Button, CopyButton, Icon } from '../ui/components.js';
import { SettingsField } from './settings-fields.js';
import { AccountPanel } from './settings-account.js';
import { SyncResetAction, ConfigFilePanel } from './settings-panels.js';
import { FactRows } from './settings-facts.js';
import { HooksList } from './settings-hooks.js';
import { SkipExistingField } from '../ui/skip-pref.js';

/** The DOM id of a sub-group's block. */
export const groupDomId = (id) => `settings-group-${id}`;

/**
 * `form` carries the page state every field needs: valueOf, dirtyKeys,
 * errors, warnings, setValue, revert, showKeys. `after` maps a key to a
 * line under its row, `extra` to more props for its control, `insert` to a
 * row placed after it.
 */
function FieldRows({ fields, form, after = {}, extra = {}, insert = {} }) {
  if (fields.length === 0) return null;
  return html`<div class="field-list">
    ${fields.map((f) => [
      html`<${SettingsField}
        key=${f.key}
        field=${f}
        value=${form.valueOf(f.key)}
        changed=${form.dirtyKeys.includes(f.key)}
        error=${form.errors[f.key]}
        warning=${form.warnings[f.key]}
        showKeys=${form.showKeys}
        disabled=${fieldDisabled(f.key, form.valueOf)}
        after=${after[f.key]}
        extra=${extra[f.key]}
        onChange=${(v) => form.setValue(f.key, v)}
        onRevert=${() => form.revert(f.key)}
      />`,
      insert[f.key] || null,
    ])}
  </div>`;
}

const STORAGE_NAMES = { 'download.downloads_dir': 'downloads_dir', 'download.temp_dir': 'temp_dir' };

/**
 * The line under a folder field, for the saved path: its free space with
 * Copy path, "Same disk as Downloads" for a temporary folder that shares
 * it, or why the folder cannot be read. It holds its room until /system
 * answers.
 */
function FolderStatus({ fieldKey, storage }) {
  const entry = storage ? storage.find((d) => d.name === STORAGE_NAMES[fieldKey]) : null;
  if (!entry) return html`<span class="folder-status" aria-hidden="true"> </span>`;
  if (entry.error) {
    return html`<span class="folder-status" data-role="warning"><${Icon} name="triangle-alert" size="sm" /><span class="wrap-any">Can't read this folder: ${entry.error}</span></span>`;
  }
  const shared = fieldKey === 'download.temp_dir' && groupByDisk(storage).some((g) => g.folders.includes(entry) && g.folders.some((f) => f.name === 'downloads_dir'));
  const text = shared ? 'Same disk as Downloads' : typeof entry.free_bytes === 'number' ? `${bytes(entry.free_bytes)} free` : '';
  return html`<span class="folder-status">
    <span>${text}</span>
    ${entry.path ? html`<${CopyButton} text=${entry.path} label="Copy path" />` : null}
  </span>`;
}

/** One sub-group of a section, under its heading. */
function Group({ group, children }) {
  const titleId = `${groupDomId(group.id)}-title`;
  return html`<section class="settings-group" id=${groupDomId(group.id)} data-group=${group.id} aria-labelledby=${titleId}>
    <h3 class="section-title settings-group-title" id=${titleId}>${group.label}</h3>
    ${children}
  </section>`;
}

/** Server's facts besides the startup values: build, start and version; empty while /system is unanswered. */
function serverFacts(sys, pending) {
  if (pending) {
    return [
      { id: 'built', label: 'Built', value: { text: '' } },
      { id: 'started', label: 'Started', value: { text: '' } },
      { id: 'version', label: 'Version', value: { text: '' }, action: html`<${Button} variant="ghost" size="sm" icon="copy" aria-label="Copy version" disabled />` },
    ];
  }
  if (!sys) return [];
  return [
    sys.build_date ? { id: 'built', label: 'Built', value: { text: absTime(sys.build_date) } } : null,
    sys.started_at ? { id: 'started', label: 'Started', value: { text: absTime(sys.started_at) } } : null,
    sys.version
      ? { id: 'version', label: 'Version', value: { text: sys.version, mono: true }, title: sys.commit ? `Commit ${sys.commit}` : undefined, action: html`<${CopyButton} text=${sys.version} label="Copy version" />` }
      : null,
  ].filter(Boolean);
}

/**
 * The body for `section`. `cfg` is the page's config load state ({loaded,
 * error, base, persisted, reloadError}); `onRetry` loads it again and
 * `onReloaded` takes a ConfigResponse. `st` is the status store's entries,
 * `now` the shared clock, `offline` whether immediate actions are off.
 */
export function SectionBody({ section, cfg, form, dirty, onRetry, onReloaded, st, now, offline }) {
  const fields = sectionFields(section.id);
  const sys = st.system && st.system.data;
  const sysPending = !st.system;
  let rows = null;
  if (cfg.error && !cfg.loaded)
    rows =
      cfg.error.status === 404
        ? html`<${Unavailable} what="Runtime configuration (GET /api/v1/config)" />`
        : html`<${ErrorNote} title="Couldn't load the settings" error=${cfg.error} onRetry=${onRetry} />`;
  else if (!cfg.loaded && fields.length) rows = html`<${SkeletonRows} n=${Math.min(fields.length, 6)} kind="line" />`;
  const fieldRows = (list, after, extra, insert) => rows || html`<${FieldRows} fields=${list} form=${form} after=${after} extra=${extra} insert=${insert} />`;

  switch (section.id) {
    case 'account': {
      const token = tokenState(cfg.loaded ? cfg.base['catalog.media_user_token'] || '' : undefined, st.wrapper);
      const syncWaits = cfg.loaded && cfg.base['library_sync.enabled'] === true && token.known && !token.present;
      return html`<${FactRows} section="account" system=${sys} pending=${sysPending} showKeys=${form.showKeys} wrapper=${st.wrapper && st.wrapper.data} />
        <${AccountPanel}>
          <section class="account-block" aria-label="Media user token">
            ${fieldRows(fields, undefined, { 'catalog.media_user_token': { wrapperToken: token.wrapperToken } })}
            ${syncWaits
              ? html`<p class="field-warning settings-token-wait" data-role="warning">
                  <${Icon} name="triangle-alert" size="sm" /><span>Library sync is waiting for this token. <a class="link" href=${settingsHash('library_sync')}>Library sync</a></span>
                </p>`
              : null}
          </section>
        </${AccountPanel}>`;
    }
    case 'files': {
      const storage = sys && Array.isArray(sys.storage) ? sys.storage : null;
      const after = Object.fromEntries(Object.keys(STORAGE_NAMES).map((k) => [k, html`<${FolderStatus} fieldKey=${k} storage=${storage} />`]));
      return html`<${Notice} role="warning" icon="triangle-alert">
          Changing a folder or a template changes where new downloads are saved. Tracks already saved under the old names are
          not recognized as downloaded, so they download again.
        </${Notice}>
        ${fieldRows(fields, after, undefined, { 'download.force_overwrite': html`<${SkipExistingField} key="local-skip" />` })}`;
    }
    case 'jobs':
      return html`<${FactRows} section="jobs" system=${sys} pending=${sysPending} showKeys=${form.showKeys} />${fieldRows(fields)}`;
    case 'library_sync':
      return html`${fieldRows(fields)}<${SyncResetAction} sync=${st.librarySync && st.librarySync.data} disabled=${offline} />`;
    case 'hooks': {
      const h = st.hooks && st.hooks.data;
      const entries = h && Array.isArray(h.entries) ? h.entries : [];
      return html`<${FactRows} section="hooks" system=${sys} pending=${sysPending} showKeys=${form.showKeys} />
        <${HooksList} entries=${entries} outcomes=${st.stats && st.stats.data ? st.stats.data.hooks : []} now=${now} />`;
    }
    case 'logging':
      return html`<${FactRows} section="logging" system=${sys} pending=${sysPending} showKeys=${form.showKeys} />${fieldRows(fields)}`;
    case 'server':
      return html`<${FactRows} section="server" system=${sys} pending=${sysPending} showKeys=${form.showKeys} extra=${serverFacts(sys, sysPending)} />
        ${SETTINGS_GROUPS.filter((g) => g.section === 'server').map(
          (g) => html`<${Group} key=${g.id} group=${g}>
            ${g.id === 'config_file'
              ? html`<${ConfigFilePanel} persisted=${cfg.persisted} reloadError=${cfg.reloadError} dirty=${dirty} base=${cfg.base} onReloaded=${onReloaded} disabled=${offline} />`
              : fieldRows(fields.filter((f) => f.group === g.id))}
          </${Group}>`,
        )}`;
    default:
      return fieldRows(fields);
  }
}
