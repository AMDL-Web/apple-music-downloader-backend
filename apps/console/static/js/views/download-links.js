// New download: the detected links. Each row shows at once what the link
// is from lib/links.js, then the lookup preview when it arrives: a 48 px
// cover, the title with the Explicit and Apple Digital Master marks, one
// meta line (kind, artist, tracks, year), any reason it cannot be sent or
// needs attention, and an earlier job of the same item. The storefront is
// shown only where it is not the server's default or was borrowed from
// another link; the URL stays in the row's tooltip. Each row that can be
// sent has a checkbox; one already in the library or the queue starts
// unchecked (lib/lookup.js isPicked). A row whose qualities were read
// says what it will download for that link; the dock says what the chosen
// quality asks for. The page owns the lookup queues and the picks; rows read them. The
// first ROWS_SHOWN rows are listed, plus every one that needs a look (an
// issue, an earlier job still queued or running), until Show all.

import { html, useState } from '../ui/preact.js';
import { artPalette, paletteStyle, shortDate, artWidths, linkLabel } from '../lib/format.js';
import { kindLabel, previewFacts, needsAttention, ROWS_SHOWN, LOOKUP_LIMIT } from '../lib/lookup.js';
import { Card, Button, Icon, Artwork, Skeleton } from '../ui/components.js';

/** Quality words with their "24-bit / 44.1 kHz" kept on one line. */
const keepSpec = (text) => {
  const m = /^(.*?)(\d+-bit \/ [\d.]+\skHz)(.*)$/.exec(text);
  return m ? [m[1], html`<span class="new-spec">${m[2]}</span>`, m[3]] : text;
};

/** A bare link that wraps after its slashes rather than inside a word. */
const breakable = (text) => text.split('/').map((part, i, all) => (i < all.length - 1 ? [part, '/', html`<wbr />`] : part));

// Types the quality probe is quick and meaningful for: one release. An
// artist or station would read every track of a whole catalog.
const QUALITY_TYPES = ['album', 'song'];
const LINK_ART = artWidths(48);

/**
 * Props: entries (scanLinks), statusOf(url) (the lookup status, with
 * 'idle' for a link past the lookup limit), issueOf(entry), priorOf(url)
 * (an earlier job, lib/lookup.js priorJob), picked(url) (the checkbox),
 * skipped (links past the lookup limit that would otherwise be looked up),
 * and row: {jobHref(id), onRemove(url), onRetry(url),
 * onQuality(entry, data), onPick(url, checked), storefront (the server's default),
 * probed(url) ({text, warning} once the link's qualities were read, else
 * null)}.
 */
export function LinksCard({ entries, statusOf, issueOf, priorOf, picked, skipped, row }) {
  const [all, setAll] = useState(false);
  const shown = all ? entries : entries.filter((e, i) => i < ROWS_SHOWN || needsAttention(issueOf(e), priorOf(e.url)));
  const hidden = entries.length - shown.length;
  // Hidden rows only have, at most, a quiet "In your library" note.
  const hiddenPrior = all ? 0 : entries.filter((e) => !shown.includes(e) && priorOf(e.url)).length;
  return html`<${Card} section="links" className="card-flush new-links-card" title="Detected links" labelId="new-links-title">
    <ul class="new-links" aria-labelledby="new-links-title">
      ${shown.map(
        (e) => html`<${LinkRow}
          key=${e.url}
          entry=${e}
          status=${statusOf(e.url)}
          issue=${issueOf(e)}
          prior=${priorOf(e.url)}
          picked=${picked(e.url)}
          row=${row}
        />`,
      )}
    </ul>
    ${hidden > 0 || (all && entries.length > ROWS_SHOWN)
      ? html`<div class="new-links-all">
          <${Button} size="sm" icon=${all ? 'chevron-up' : 'chevron-down'} className="new-links-toggle" aria-expanded=${String(all)} onClick=${() => setAll(!all)}
            >${all ? `Show the first ${ROWS_SHOWN}` : `Show all ${entries.length} links`}</${Button}>
          ${!all ? html`<span class="new-hint">${hidden} more not listed, ${hiddenPrior ? `${hiddenPrior} in your library` : 'none with a problem so far'}.</span>` : null}
        </div>`
      : null}
    ${skipped > 0
      ? html`<p class="new-links-more">
          <${Icon} name="info" size="sm" />
          <span>Links are checked up to the first ${LOOKUP_LIMIT}; ${skipped} more ${skipped === 1 ? 'is' : 'are'} downloaded without a check.</span>
        </p>`
      : null}
  </${Card}>`;
}

/**
 * The quiet note for an earlier job: in the queue now, or in the library
 * since when, with Open. It breaks only between its parts.
 */
function PriorNote({ prior, href }) {
  const active = prior.state === 'active';
  const when = active ? '' : shortDate(prior.job.finished_at);
  const text = [active ? 'In the queue now' : 'In your library', when].filter(Boolean).map((p) => p.replaceAll(' ', '\u00a0'));
  return html`<span class="new-link-prior" data-state=${prior.state}>
    <${Icon} name=${active ? 'clock' : 'circle-check'} size="sm" />
    <span>${text.join(' · ')} · <a class="link new-link-open" href=${href} title="Open that job">Open</a></span>
  </span>`;
}

function LinkRow({ entry, status, issue, prior, picked, row }) {
  const link = entry.link;
  const data = status.state === 'ready' ? status.data : null;
  const facts = previewFacts(data);
  const palette = data ? artPalette(data) : null;
  const kind = link ? link.kind : 'invalid';
  const typeText = data && data.type ? kindLabel(data.type) : link ? kindLabel(link.kind, link.trackId) : 'Not Apple Music';
  const loading = status.state === 'loading' || status.state === 'queued';
  // A row with no preview is titled by its link, cut like a Queue row's so
  // rows of the same storefront never read the same up to an ellipsis.
  const name = facts ? facts.title : linkLabel(entry.url);
  const cls = ['new-link', palette ? 'has-palette' : ''].filter(Boolean).join(' ');
  const blocked = !link || Boolean(issue && issue.blocking);
  const artist = kind === 'artist' && !blocked;
  // The storefront only where it says something: not the server's default,
  // or borrowed from the other links.
  const sf = link && link.storefront && (link.noStorefront || link.storefront !== (row.storefront || '').toLowerCase()) ? link.storefront : '';
  const sfTitle = link && link.noStorefront ? 'Storefront taken from the other links, since this link has none' : 'Storefront';
  const meta = [
    { key: 'type', text: typeText },
    facts && facts.sub ? { key: 'artist', text: facts.sub, auto: true } : null,
    facts && facts.album ? { key: 'album', text: facts.album, auto: true } : null,
    ...(facts ? facts.facts.filter((f) => f.key !== 'type' && f.key !== 'genre') : []),
    artist ? { key: 'scope', text: 'Whole discography' } : null,
  ].filter(Boolean);
  const probed = data && !blocked ? row.probed(entry.url) : null;
  return html`<li
    class=${cls}
    title=${entry.url}
    data-kind=${kind}
    data-state=${link ? status.state : 'invalid'}
    data-issue=${issue ? issue.code : undefined}
    data-blocking=${issue && issue.blocking ? '' : undefined}
    data-excluded=${blocked || !picked ? '' : undefined}
    aria-busy=${loading ? 'true' : undefined}
    style=${paletteStyle(palette)}
  >
    <label class="new-link-pick">
      <input
        type="checkbox"
        class="job-check-input"
        checked=${!blocked && picked}
        disabled=${blocked}
        aria-label=${blocked ? `${name}: can't be downloaded` : `Download ${name}`}
        title=${blocked ? "Can't be downloaded" : undefined}
        onChange=${(e) => row.onPick(entry.url, e.currentTarget.checked)}
      />
    </label>
    ${data
      ? html`<${Artwork} url=${data.artwork_url} bg=${palette && palette.bg} px=${100} widths=${LINK_ART} sizes="48px" className="art-48 new-link-art" />`
      : html`<div class="art art-48 new-link-art" aria-hidden="true">
          ${loading ? html`<${Skeleton} kind="tile" />` : html`<span class="art-glyph"><${Icon} name=${link ? 'music' : 'circle-slash'} /></span>`}
        </div>`}
    <div class="new-link-main">
      <span class="new-link-head">
        <span class=${facts ? 'new-link-title' : loading ? 'new-link-title is-url is-pending' : 'new-link-title is-url'} dir="auto">${facts ? name : breakable(name)}</span>
        ${facts && facts.explicit ? html`<span class="new-link-explicit" title="Explicit" aria-label="Explicit">E</span>` : null}
        ${facts && facts.adm ? html`<span class="chip new-link-adm" title="Mastered for Apple's high-resolution formats">Apple Digital Master</span>` : null}
        ${sf ? html`<span class="chip new-link-sf" title=${sfTitle}>${sf.toUpperCase()}</span>` : null}
      </span>
      <span class="new-link-sub" title=${meta.map((m) => m.text).join(' · ')}>
        ${meta.map((m) => html`<span key=${m.key} class="new-link-fact" data-key=${m.key} dir=${m.auto ? 'auto' : undefined}>${m.text}</span>`)}
        ${loading ? html`<span class="new-link-loading">Looking up…</span>` : null}
      </span>
      ${status.state === 'idle' && link && !issue ? html`<span class="new-link-note">Not checked</span>` : null}
      ${link && link.noStorefront && link.storefront && !issue
        ? html`<span class="new-link-note" data-note="storefront">No storefront in the link: read as ${link.storefront.toUpperCase()}, like the other links</span>`
        : null}
      ${prior && !blocked ? html`<${PriorNote} prior=${prior} href=${row.jobHref(prior.job.id)} />` : null}
      ${probed
        ? html`<span class="new-link-will">
            <span>Will download: ${keepSpec(probed.text)}</span>
            ${probed.warning
              ? html`<span class="status-text" data-role="warning"><${Icon} name="triangle-alert" size="sm" /><span>${probed.warning}</span></span>`
              : null}
          </span>`
        : null}
      ${issue
        ? html`<span class="new-link-issue status-text" data-role=${issue.role} title=${issue.title || undefined}>
            <${Icon} name=${issue.role === 'danger' ? 'circle-x' : 'triangle-alert'} size="sm" />
            <span>${issue.message}${issue.blocking ? html`<span class="sr-only">. Not downloaded.</span>` : null}${issue.href
              ? html`. <a class="link new-link-fix" href=${issue.href}>${issue.action}</a>`
              : null}</span>
          </span>`
        : null}
    </div>
    <div class="new-link-actions">
      ${data && QUALITY_TYPES.includes(data.type)
        ? html`<${Button}
            size="sm"
            icon="disc"
            className="new-link-quality"
            aria-label="Available qualities"
            title="See which formats each track offers"
            onClick=${() => row.onQuality(entry, data)}
          >Available qualities</${Button}>`
        : null}
      ${status.state === 'error' && issue && !issue.blocking
        ? html`<${Button} size="sm" icon="rotate" className="new-link-retry" onClick=${() => row.onRetry(entry.url)}>Retry</${Button}>`
        : null}
      <${Button} variant="ghost" size="sm" icon="x" className="new-link-remove" aria-label=${`Remove ${name}`} title="Remove from the list" onClick=${() => row.onRemove(entry.url)} />
    </div>
  </li>`;
}
