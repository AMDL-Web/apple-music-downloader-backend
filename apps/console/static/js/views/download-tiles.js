// New download: the search hits drawn as Overview's tile wall (css/overview.css
// .wall-tile): an album or a playlist is its cover with Download over it,
// then title and artist (a playlist's curator); an artist is a round cover
// that opens its albums. A hit in the
// library or the queue marks its cover, and the cover opens that job.

import { html } from '../ui/preact.js';
import { artPalette, paletteStyle, artWidths } from '../lib/format.js';
import { hitView } from '../lib/search.js';
import { Icon, Artwork } from '../ui/components.js';

const TILE_ART = artWidths(232);
const TILE_SIZES = '(max-width: 639px) 45vw, 232px';

/**
 * An album or playlist hit. row: {busy, jobHref(id), onDownload(hit), priorOf(hit)}.
 * Download (data-hit-primary) is the tile's one focus stop, aria-disabled
 * (not disabled, so focus stays on it) while it is sent and once the hit
 * is in the queue.
 */
export function AlbumTile({ hit, row }) {
  const v = hitView(hit);
  const palette = artPalette(hit);
  const prior = row.priorOf(hit);
  const queued = prior && prior.state === 'active';
  const busy = row.busy.has(hit.url);
  const sub = v.facts.map((f) => f.text).join(' · ');
  const cover = html`<${Artwork} url=${hit.artwork_url} bg=${palette && palette.bg} px=${464} widths=${TILE_ART} sizes=${TILE_SIZES} className="art-square" />`;
  return html`<li class=${palette ? 'new-tile has-palette' : 'new-tile'} data-kind=${hit.type} data-id=${hit.id} data-prior=${prior ? prior.state : undefined} style=${paletteStyle(palette)}>
    <div class="wall-tile">
      <div class="new-tile-cover">
        ${prior
          ? html`<a class="new-tile-job" href=${row.jobHref(prior.job.id)} tabindex="-1" title=${queued ? 'In the queue now: open that job' : 'In your library: open that job'}>${cover}</a>`
          : cover}
        ${v.quality[0] ? html`<span class="new-tile-chip">${v.quality[0]}</span>` : null}
        ${prior
          ? html`<span class="new-tile-badge" data-state=${prior.state} role="img" aria-label=${queued ? 'In the queue' : 'In your library'}>
              <${Icon} name=${queued ? 'clock' : 'circle-check'} size="sm" />
            </span>`
          : null}
        <button
          type="button"
          class="new-tile-go"
          data-hit-primary
          aria-disabled=${busy || queued ? 'true' : undefined}
          aria-busy=${busy ? 'true' : undefined}
          aria-label=${queued ? `${v.title} is in the queue` : `Download ${v.title}`}
          title=${queued ? 'Already in the queue' : prior ? 'In your library; tracks on disk are skipped unless Overwrite is on' : 'Download'}
          onClick=${() => busy || queued || row.onDownload(hit)}
        ><${Icon} name="download" /></button>
      </div>
      <span class="wall-title new-tile-title" title=${v.title}>
        <span class="new-tile-name" dir="auto">${v.title}</span>
        ${v.explicit ? html`<span class="new-link-explicit" title="Explicit" aria-label="Explicit">E</span>` : null}
      </span>
      <span class="wall-sub new-tile-sub" title=${sub}>
        ${v.facts.map((f) => html`<span key=${f.key} class="new-tile-fact" dir=${f.auto ? 'auto' : undefined}>${f.text}</span>`)}
      </span>
    </div>
  </li>`;
}

/** An artist hit: one button, the round cover and the name, that opens its albums. */
export function ArtistTile({ hit, row }) {
  const v = hitView(hit);
  const palette = artPalette(hit);
  return html`<li class=${palette ? 'new-tile has-palette' : 'new-tile'} data-kind="artist" data-id=${hit.id} style=${paletteStyle(palette)}>
    <button type="button" class="wall-tile new-tile-open" data-hit-primary title=${`Show the albums of ${v.title}`} onClick=${() => row.onOpenArtist(hit)}>
      <${Artwork} url=${hit.artwork_url} bg=${palette && palette.bg} px=${464} widths=${TILE_ART} sizes=${TILE_SIZES} className="art-square" />
      <span class="wall-title" dir="auto">${v.title}</span>
      <span class="wall-sub" dir=${hit.genre ? 'auto' : undefined}>${v.facts[0] ? v.facts[0].text : 'Artist'}</span>
    </button>
  </li>`;
}

/** Placeholder tiles while the first answer loads, round for artists; `expanded` as a wall that does not scroll sideways on a phone. */
export function SkeletonTiles({ n, kind, expanded }) {
  return html`<ul class="wall new-wall" data-kind=${kind} data-expanded=${expanded ? '' : undefined} aria-hidden="true">
    ${Array.from(
      { length: n },
      (_, i) => html`<li key=${i} class="new-tile" data-kind=${kind}>
        <span class="wall-tile is-skel">
          <span class="art art-square skel"></span>
          <span class="wall-title"><span class="skel skel-text"> </span></span>
          <span class="wall-sub"><span class="skel skel-text"> </span></span>
        </span>
      </li>`,
    )}
  </ul>`;
}
