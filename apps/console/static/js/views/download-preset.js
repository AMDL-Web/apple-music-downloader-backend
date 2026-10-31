// New download: the quality controls under the text box. A preset menu
// (lib/overrides.js PRESETS, the custom values once set, and Custom…) and
// the Custom… sheet that edits the codec order, the fallback and the ALAC
// limits for this download only. The page owns the choice and turns it into
// overrides (lib/overrides.js presetOverrides). Files already on disk are
// asked about when a download needs it (views/download-ask.js).

import { html, useState } from '../ui/preact.js';
import { PRESETS, QUALITY_KEYS, SAMPLE_RATES, BIT_DEPTHS, choices, rateLabel, customLabel } from '../lib/overrides.js';
import { fieldByKey } from '../lib/settings.js';
import { Button } from '../ui/components.js';
import { Dialog } from '../ui/dialogs.js';
import { useMedia } from '../ui/hooks.js';
import { ListEditor } from './settings-list.js';

// The menu item that opens the sheet. It is never the selected value, so
// choosing it again while Custom is active opens the sheet again.
const EDIT = 'custom-edit';

/**
 * Props: defaults (lib/overrides.js optionDefaults, null until GET config
 * answers), preset (a PRESETS id or 'custom'), custom (the custom values),
 * asks (what the choice asks for in words, the menu's tooltip; the dock
 * prints it), onPreset(id), onCustom() (opens the sheet). The closed menu
 * shows only the preset's name, so it fits a phone. Until the config loads
 * only Follow Settings can be chosen.
 */
export function QualityControls({ defaults, preset, custom, asks, onPreset, onCustom }) {
  const ready = Boolean(defaults);
  const wait = ready ? undefined : 'Loading Settings…';
  const onChange = (e) => {
    const v = e.currentTarget.value;
    if (v !== EDIT) {
      onPreset(v);
      return;
    }
    e.currentTarget.value = preset;
    onCustom();
  };
  return html`<div class="new-controls">
    <span class="new-quality">
      <label class="sr-only" for="new-quality">Quality</label>
      <select id="new-quality" class="select new-quality-select" value=${preset} title=${asks || undefined} onChange=${onChange}>
        ${PRESETS.map(
          (p) => html`<option key=${p.id} value=${p.id} disabled=${!ready && p.id !== 'follow'} title=${p.id === 'follow' ? undefined : wait}>${p.label}</option>`,
        )}
        ${preset === 'custom' ? html`<option key="custom" value="custom">${customLabel(custom)}</option>` : null}
        <option key=${EDIT} value=${EDIT} disabled=${!ready} title=${wait}>Custom…</option>
      </select>
    </span>
  </div>`;
}

const pick = (values) => {
  const out = {};
  for (const key of QUALITY_KEYS) out[key] = Array.isArray(values[key]) ? values[key].slice() : values[key];
  return out;
};

/**
 * The Custom… sheet: a popover-sized dialog (a bottom sheet under 640 px)
 * that opens with its heading focused, so no control changes on the next
 * key, and returns focus to the menu when it closes. Props: defaults,
 * start (the values it opens with), onDone(values), onFollow() (back to
 * Quality: Settings), onClose() (Escape, the scrim, Close: nothing changes).
 */
export function CustomSheet({ defaults, start, onDone, onFollow, onClose }) {
  const phone = useMedia('(max-width: 639px)');
  const [draft, setDraft] = useState(() => pick(start));
  const set = (key, v) => setDraft((d) => ({ ...d, [key]: v }));
  const num = (e) => Number.parseInt(e.currentTarget.value, 10);
  const rate = fieldByKey('download.alac_max_sample_rate');
  const depth = fieldByKey('download.alac_max_bit_depth');
  return html`<${Dialog} name="custom" className="sheet sheet-custom" placement=${phone ? 'bottom' : 'center'} labelledBy="new-custom-title" onClose=${onClose}>
    <div class="sheet-head">
      <h2 id="new-custom-title" class="card-title" tabindex="-1" data-autofocus>Custom quality</h2>
      <${Button} variant="ghost" size="sm" icon="x" aria-label="Close" onClick=${onClose} />
    </div>
    <div class="new-custom-body">
      <div class="new-custom-group">
        <span class="new-custom-label" id="new-custom-codecs-label">Codec priority</span>
        <${ListEditor}
          field=${fieldByKey('download.quality_priority')}
          id="new-custom-codecs"
          value=${draft.quality_priority}
          onChange=${(v) => set('quality_priority', v)}
        />
      </div>
      <label class="new-custom-switch">
        <span>Fall back to the next codec</span>
        <input type="checkbox" role="switch" class="field-switch" checked=${draft.codec_alternative} onChange=${(e) => set('codec_alternative', e.currentTarget.checked)} />
      </label>
      ${draft.quality_priority.includes('alac')
        ? html`<div class="new-custom-limits">
            <label class="new-custom-field">
              <span>${rate.label}</span>
              <select class="select" value=${String(draft.alac_max_sample_rate)} onChange=${(e) => set('alac_max_sample_rate', num(e))}>
                ${choices(SAMPLE_RATES, defaults.alac_max_sample_rate).map((v) => html`<option key=${v} value=${String(v)}>${rateLabel(v)}</option>`)}
              </select>
            </label>
            <label class="new-custom-field">
              <span>${depth.label}</span>
              <select class="select" value=${String(draft.alac_max_bit_depth)} onChange=${(e) => set('alac_max_bit_depth', num(e))}>
                ${choices(BIT_DEPTHS, defaults.alac_max_bit_depth).map((v) => html`<option key=${v} value=${String(v)}>${v}-bit</option>`)}
              </select>
            </label>
          </div>`
        : null}
    </div>
    <div class="dialog-actions new-custom-actions">
      <${Button} className="new-custom-follow" onClick=${onFollow}>Use Settings</${Button}>
      <${Button} variant="primary" className="new-custom-done" onClick=${() => onDone(draft)}>Done</${Button}>
    </div>
  </${Dialog}>`;
}
