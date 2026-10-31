// New download: one search group shown alone (#/new?q=&group=), opened by
// its Show more. Its title row becomes Back to every group and the four
// groups as a segmented control; the group's hits follow page after page
// (lib/search.js SEARCH_MORE) as the end of the list comes within 600 px of
// the viewport, and a foot row of one height says a page is loading or
// failed, with Retry. download-search.js places it and handles its keys.

import { html, useEffect, useRef } from '../ui/preact.js';
import { SEARCH_GROUPS, SEARCH_FIRST, groupFoot } from '../lib/search.js';
import { Card, Button, Icon, Segmented } from '../ui/components.js';
import { AlbumTile, ArtistTile, SkeletonTiles } from './download-tiles.js';
import { HitRow, SkeletonHits } from './download-songs.js';

/** How far below the viewport the end of the list starts the next page. */
const LOAD_AHEAD = '0px 0px 600px 0px';

const TABS = SEARCH_GROUPS.map((g) => ({ value: g.id, label: g.label }));

/** The foot under the list: one height whether it waits, loads or failed. */
function Foot({ state, footRef, onRetry }) {
  return html`<div class="new-search-foot" ref=${footRef} data-state=${state}>
    ${state === 'loading' ? html`<span class="new-search-foot-text">Loading more…</span>` : null}
    ${state === 'error'
      ? html`<span class="status-text" data-role="warning"><${Icon} name="triangle-alert" size="sm" /><span>Couldn't load more</span></span>
          <${Button} size="sm" icon="rotate" className="new-search-foot-retry" onClick=${onRetry}>Retry</${Button}>`
      : null}
  </div>`;
}

/**
 * Props: group (a SEARCH_GROUPS id), data (the search answer), term,
 * loading (no answer yet), stale (a new term loads), more (lib/search.js
 * createSearcher state().more), row (as download-search.js), onGroup(id),
 * onAll() (every group again), onMore(id) (the next page).
 */
export function GroupView({ group, data, term, loading, stale, more, row, onGroup, onAll, onMore }) {
  const meta = SEARCH_GROUPS.find((g) => g.id === group);
  const g = data && data[group];
  const items = g && Array.isArray(g.items) ? g.items : [];
  const foot = loading ? '' : groupFoot(g, more[group]);
  const songs = group === 'songs';
  const kind = group === 'artists' ? 'artist' : group === 'playlists' ? 'playlist' : 'album';
  const Tile = kind === 'artist' ? ArtistTile : AlbumTile;

  // The next page is read once the foot comes within LOAD_AHEAD of the
  // viewport. A new observer after every page: its first callback reports
  // where the foot is now, so a short page that leaves the foot in reach
  // reads the next one too. A failed page waits for Retry.
  const footRef = useRef(null);
  const want = !stale && foot === 'wait';
  useEffect(() => {
    const el = footRef.current;
    if (!want || !el || typeof IntersectionObserver !== 'function') return undefined;
    const io = new IntersectionObserver((entries) => entries.some((e) => e.isIntersecting) && onMore(group), { rootMargin: LOAD_AHEAD });
    io.observe(el);
    return () => io.disconnect();
  }, [want, group, items.length]);

  let body;
  if (loading) {
    body = songs ? html`<${SkeletonHits} n=${SEARCH_FIRST} />` : html`<${SkeletonTiles} n=${SEARCH_FIRST} kind=${kind} expanded />`;
  } else if (!items.length && !(g && g.next_offset)) {
    body = null;
  } else if (songs) {
    body = html`<ul class="new-hits" aria-label=${meta.label}>
      ${items.map((hit, i) => html`<${HitRow} key=${i} hit=${hit} row=${row} />`)}
    </ul>`;
  } else {
    body = html`<ul class="wall new-wall" data-kind=${kind} data-expanded="" aria-label=${meta.label}>
      ${items.map((hit, i) => html`<${Tile} key=${i} hit=${hit} row=${row} />`)}
    </ul>`;
  }

  return html`<div class="new-search-alone">
    <div class="new-group-head">
      <${Button} variant="ghost" size="sm" icon="chevron-left" className="new-group-back" aria-label="All results" title="All results" onClick=${onAll} />
      <${Segmented} label="Result group" options=${TABS} value=${group} onChange=${(id) => id !== group && onGroup(id)} />
    </div>
    ${body && songs ? html`<${Card} section="hits-songs" className="card-flush new-search-group">${body}</${Card}>` : null}
    ${body && !songs ? html`<section class="new-search-group" data-section=${`hits-${group}`}>${body}</section>` : null}
    ${body
      ? null
      : html`<p class="new-search-note new-search-empty">
          <${Icon} name="search" size="sm" />
          <span>No ${meta.many} for “<span dir="auto">${term}</span>”</span>
        </p>`}
    ${foot ? html`<${Foot} state=${foot} footRef=${footRef} onRetry=${() => onMore(group)} />` : null}
  </div>`;
}
