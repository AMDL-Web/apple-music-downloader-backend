// New download: catalog search, for a text box that holds words and no link.
// The first hits come in four titled sections: Albums, Playlists and
// Artists as tile walls (download-tiles.js), Songs as rows on a grouped
// surface (download-songs.js). A section's Show more shows that group alone
// under tabs, page after page (download-search-group.js); an artist opens
// its albums in their place with Back and Download all.
// ArrowDown from the text box enters the hits, the arrows move between
// them, Return downloads or opens, Escape leaves. While a new term loads,
// the last hits stay (dimmed) and the room never shrinks during a search,
// so nothing under it jumps as results come and go. The state is in
// download-search-state.js, the logic pure in lib/search.js; the page
// (download.js) submits what Download sends.

import { html, useState, useRef, useLayoutEffect } from '../ui/preact.js';
import { artPalette, paletteStyle, artWidths } from '../lib/format.js';
import { shownHits, hasMore, nextStop, SEARCH_GROUPS, SEARCH_FIRST, SEARCH_MAX_TERM } from '../lib/search.js';
import { hitKey } from '../lib/search-jobs.js';
import { shortError } from '../lib/lookup.js';
import { Card, Button, Icon, Artwork, Skeleton, Notice } from '../ui/components.js';
import { AlbumTile, ArtistTile, SkeletonTiles } from './download-tiles.js';
import { HitRow, SkeletonHits } from './download-songs.js';
import { GroupView } from './download-search-group.js';

const ARTIST_ART = artWidths(120);

// Placeholders per group while the first answer loads: a typical answer,
// so the page is about its final height when the hits arrive.
const SKELETON_ROWS = { songs: SEARCH_FIRST, albums: SEARCH_FIRST, playlists: SEARCH_FIRST, artists: 2 };

/** A failed read: what failed, the server's reason without the catalog prefix it repeats, Retry. */
function FailNote({ what, error, onRetry }) {
  const why = shortError(String((error && error.message) || '').replace(/^read Apple Music catalog:\s*/i, ''));
  return html`<div class="new-search-fail" role="alert">
    <${Notice} role="danger" icon="circle-x" action=${html`<${Button} size="sm" icon="rotate" className="new-search-retry" onClick=${onRetry}>Retry</${Button}>`}>
      <span class="new-search-fail-text"><strong>${what}.</strong> ${why}</span>
    </${Notice}>
  </div>`;
}

/** Show more under an artist's albums, its loading state, or its failure with Retry. */
function MoreRow({ state, label, onMore }) {
  if (state === 'error') {
    return html`<div class="new-search-more">
      <span class="status-text" data-role="warning"><${Icon} name="triangle-alert" size="sm" /><span>Couldn't load more</span></span>
      <${Button} size="sm" icon="rotate" onClick=${onMore}>Retry</${Button}>
    </div>`;
  }
  return html`<div class="new-search-more">
    <${Button} size="sm" icon="chevron-down" className="new-search-more-btn" disabled=${state === 'loading'} aria-busy=${state === 'loading' ? 'true' : undefined} onClick=${onMore}
      >${state === 'loading' ? 'Loading…' : label}</${Button}>
  </div>`;
}

// Hits are keyed by position: a new term's hits take the places the last
// term's had, in place, instead of the old ones moving to new places.
// A section shows its first SEARCH_FIRST hits, also after pages were read
// for it alone; its Show more (red text, as Overview's actions) shows it
// alone.
function Groups({ data, loading, row, onGroup }) {
  return SEARCH_GROUPS.map((g) => {
    const group = data && data[g.id];
    if (!loading && !(group && group.items.length)) return null;
    const items = group ? group.items.slice(0, SEARCH_FIRST) : [];
    const songs = g.id === 'songs';
    const kind = g.id === 'artists' ? 'artist' : g.id === 'playlists' ? 'playlist' : 'album';
    const Tile = kind === 'artist' ? ArtistTile : AlbumTile;
    const label = `new-search-${g.id}`;
    let body;
    if (loading) {
      body = songs ? html`<${SkeletonHits} n=${SKELETON_ROWS[g.id]} />` : html`<${SkeletonTiles} n=${SKELETON_ROWS[g.id]} kind=${kind} />`;
    } else if (songs) {
      body = html`<ul class="new-hits" aria-labelledby=${label}>
        ${items.map((hit, i) => html`<${HitRow} key=${i} hit=${hit} row=${row} />`)}
      </ul>`;
    } else {
      body = html`<ul class="wall new-wall" data-kind=${kind} aria-labelledby=${label}>
        ${items.map((hit, i) => html`<${Tile} key=${i} hit=${hit} row=${row} />`)}
      </ul>`;
    }
    return html`<${Card}
      key=${g.id}
      section=${`hits-${g.id}`}
      className=${songs ? 'card-flush new-search-group' : 'new-search-group'}
      title=${g.label}
      labelId=${label}
      actions=${!loading && hasMore(group)
        ? html`<${Button} variant="ghost" className="new-search-more-btn" aria-label=${`Show more ${g.many}`} onClick=${() => onGroup(g.id)}>Show more</${Button}>`
        : null}
    >${body}</${Card}>`;
  });
}

function ArtistView({ artist, hit, row, onBack, onMore, onRetry, onDownloadAll }) {
  const shown = artist.artist || hit;
  const palette = shown ? artPalette(shown) : null;
  const name = shown ? shown.name : '';
  return html`<div class="new-artist">
    <div class=${palette ? 'new-artist-head has-palette' : 'new-artist-head'} style=${paletteStyle(palette)}>
      <${Button} variant="ghost" size="sm" icon="chevron-left" className="new-artist-back" data-hit-primary onClick=${onBack}>Results</${Button}>
      ${shown
        ? html`<${Artwork} url=${shown.artwork_url} bg=${palette && palette.bg} px=${240} widths=${ARTIST_ART} sizes="(max-width: 639px) 88px, 120px" className="new-artist-art" />`
        : html`<span class="art new-artist-art"><${Skeleton} kind="tile" /></span>`}
      <div class="new-artist-main">
        ${name ? html`<h2 class="new-artist-name" dir="auto">${name}</h2>` : html`<span class="skel skel-lh new-hit-skel-title"></span>`}
        <span class="new-artist-sub" dir=${shown && shown.genre ? 'auto' : undefined}>${shown && shown.genre ? shown.genre : 'Artist'}</span>
      </div>
      <${Button} icon="download" className="new-artist-all" disabled=${!shown} onClick=${() => onDownloadAll(shown)}>Download all</${Button}>
    </div>
    ${artist.status === 'error'
      ? html`<${FailNote} what="Couldn't load the albums" error=${artist.error} onRetry=${onRetry} />`
      : artist.status === 'ready'
        ? html`<ul class="wall new-wall" data-kind="album" aria-label=${`Albums by ${name}`}>
            ${artist.albums.items.map((a) => html`<${AlbumTile} key=${hitKey(a)} hit=${a} row=${row} />`)}
          </ul>
          ${artist.albums.items.length === 0 ? html`<p class="new-search-note">Apple Music lists no albums for this artist here.</p>` : null}
          ${artist.albums.next_offset ? html`<${MoreRow} state=${artist.more} label="Show more albums" onMore=${onMore} />` : null}`
        : html`<${SkeletonTiles} n=${SEARCH_FIRST} kind="album" />`}
  </div>`;
}

/**
 * Props: s (useSearch), term, tooLong, inputRef (the text box arrows
 * return to), openHit (the artist hit an artist view was opened from, for
 * its head before the answer), focusArtist (focus its Back on open), row: {busy (urls being sent), jobHref(id),
 * onDownload(hit), onOpenArtist(hit), priorOf(hit)}, onBack, onDownloadAll(artistHit),
 * group (the group shown alone, '' for every group), onGroup(id) (show that
 * group alone), onAllResults() (every group again).
 */
export function SearchCard({ s, term, tooLong, inputRef, openHit, focusArtist, row, onBack, onDownloadAll, group = '', onGroup, onAllResults }) {
  const { search, artist } = s;
  const viewing = artist.status !== 'idle';
  const alone = !viewing && !tooLong && Boolean(group);
  // Skeletons only while there is nothing to show; otherwise the last
  // answer stays, dimmed, through the debounce and the request.
  const haveHits = Boolean(search.data);
  const loading = !viewing && !haveHits && (s.pending || search.status === 'loading');
  const stale = !viewing && haveHits && (s.pending || search.stale);
  const empty = !viewing && !alone && !loading && search.status === 'ready' && search.data && shownHits(search.data).length === 0;

  // The room the card takes never shrinks while a term is in the box and
  // the view stays (every group, one group alone, an artist): results that
  // come back shorter, an error or "nothing found" leave canvas below the
  // card instead of pulling up everything under it. Another view starts
  // from its own height.
  const body = useRef(null);
  const room = useRef(null);
  const [floor, setFloor] = useState(0);
  const view = viewing ? `artist:${artist.id}` : group;
  const floorView = useRef(view);
  useLayoutEffect(() => {
    if (!term) {
      if (floor) setFloor(0);
      return;
    }
    const h = room.current ? Math.ceil(room.current.firstElementChild.getBoundingClientRect().height) : 0;
    if (floorView.current !== view) {
      floorView.current = view;
      if (h !== floor) setFloor(h);
    } else if (h > floor) setFloor(h);
  });

  // A group shown alone starts at its tabs, the one shown focused; back in
  // every group, that group's Show more (else its first hit) takes focus.
  // An artist opened from a group alone returns to it as it was.
  const wasGroup = useRef(alone ? group : '');
  useLayoutEffect(() => {
    if (viewing) return;
    const prev = wasGroup.current;
    const now = alone ? group : '';
    wasGroup.current = now;
    if (!body.current || now === prev) return;
    if (now) {
      const head = body.current.querySelector('.new-group-head');
      if (head && head.getBoundingClientRect().top < (parseFloat(getComputedStyle(head).scrollMarginTop) || 0)) head.scrollIntoView({ block: 'start' });
      const tab = !prev && body.current.querySelector('.new-group-head .seg-btn[aria-pressed="true"]');
      if (tab) tab.focus({ preventScroll: true });
      return;
    }
    const lost = !document.activeElement || document.activeElement === document.body || body.current.contains(document.activeElement);
    // A group with no more than its first hits has no Show more: its first hit.
    const section = lost && body.current.querySelector(`[data-section="hits-${prev}"]`);
    const el = section && (section.querySelector('.new-search-more-btn') || section.querySelector('[data-hit-primary]'));
    if (el) el.focus();
  });

  // Focus follows the view: an artist this page opened focuses its Back;
  // back in the results, the row of the artist that was open takes focus
  // when the view it had is gone.
  const was = useRef({ viewing, id: '' });
  useLayoutEffect(() => {
    const prev = was.current;
    was.current = { viewing, id: viewing ? artist.id : prev.id };
    if (!body.current || viewing === prev.viewing) return;
    const lost = !document.activeElement || document.activeElement === document.body || body.current.contains(document.activeElement);
    const el = viewing
      ? focusArtist && body.current.querySelector('.new-artist-back')
      : lost && prev.id && body.current.querySelector(`.new-tile[data-id="${prev.id}"] .new-tile-open`);
    if (el) el.focus();
  });

  // The hits are one Tab stop: the one last focused (else the first) keeps
  // tabindex 0 and the arrows move between the rest.
  const stopsOf = () => (body.current ? [...body.current.querySelectorAll('[data-hit-primary]')].filter((el) => !el.disabled && el.offsetParent !== null) : []);
  const roving = useRef(null);
  useLayoutEffect(() => {
    const stops = stopsOf();
    const keep = stops.includes(roving.current) ? roving.current : stops[0];
    for (const el of stops) el.tabIndex = el === keep ? 0 : -1;
  });
  const onFocusIn = (e) => {
    const stops = stopsOf();
    if (!stops.includes(e.target)) return;
    roving.current = e.target;
    for (const el of stops) el.tabIndex = el === e.target ? 0 : -1;
  };

  const onKeyDown = (e) => {
    if (e.altKey || e.ctrlKey || e.metaKey || !body.current) return;
    const stops = stopsOf();
    const at = stops.indexOf(document.activeElement);
    // The whole tile or row comes into view, not just its button.
    const go = (el) => {
      e.preventDefault();
      el.focus({ preventScroll: true });
      (el.closest('li') || el).scrollIntoView({ block: 'nearest', inline: 'nearest' });
    };
    const toInput = () => {
      e.preventDefault();
      const el = inputRef.current;
      if (!el) return;
      el.focus();
      el.setSelectionRange(el.value.length, el.value.length);
    };
    // Escape leaves an artist, then a group shown alone, then the hits.
    if (e.key === 'Escape') {
      if (viewing || alone) {
        e.preventDefault();
        if (viewing) onBack();
        else onAllResults();
      } else toInput();
      return;
    }
    if (at < 0 || !['ArrowDown', 'ArrowUp', 'ArrowLeft', 'ArrowRight', 'Home', 'End'].includes(e.key)) return;
    // Where each hit (its tile or row, not its button) is drawn, and which
    // section it is in.
    const boxes = stops.map((el) => {
      const r = (el.closest('li') || el).getBoundingClientRect();
      const group = el.closest('[data-section], .new-artist-head, .new-artist');
      return { top: r.top, bottom: r.bottom, left: r.left, right: r.right, group };
    });
    const to = nextStop(boxes, at, e.key);
    if (to === -1) toInput();
    else if (to !== at) go(stops[to]);
    else if (e.key.startsWith('Arrow')) e.preventDefault();
  };

  let content;
  if (tooLong) {
    content = html`<p class="new-search-note">Search terms are at most ${SEARCH_MAX_TERM} characters.</p>`;
  } else if (viewing) {
    content = html`<${ArtistView}
      artist=${artist}
      hit=${openHit}
      row=${row}
      onBack=${onBack}
      onMore=${s.artistMore}
      onRetry=${s.artistRetry}
      onDownloadAll=${onDownloadAll}
    />`;
  } else if (!loading && search.status === 'error') {
    content = html`<${FailNote} what="Couldn't search Apple Music" error=${search.error} onRetry=${s.retry} />`;
  } else if (empty) {
    content = html`<p class="new-search-note new-search-empty">
      <${Icon} name="search" size="sm" />
      <span>Nothing in Apple Music matches “<span dir="auto">${search.term}</span>”</span>
    </p>`;
  } else if (alone) {
    content = html`<${GroupView}
      group=${group}
      data=${search.data}
      term=${search.term || term}
      loading=${loading}
      stale=${stale}
      more=${search.more}
      row=${row}
      onGroup=${onGroup}
      onAll=${onAllResults}
      onMore=${s.more}
    />`;
  } else {
    content = html`<${Groups} data=${search.data} loading=${loading} row=${row} onGroup=${onGroup} />`;
  }

  return html`<div class="new-search-room" ref=${room} style=${floor ? { minHeight: `${floor}px` } : undefined}>
    <section
      class="new-search"
      ref=${body}
      aria-labelledby="new-search-title"
      data-state=${viewing ? 'artist' : loading ? 'loading' : stale ? 'stale' : search.status}
      aria-busy=${loading || stale ? 'true' : undefined}
      onKeyDown=${onKeyDown}
      onfocusin=${onFocusIn}
    >
      <h2 id="new-search-title" class="sr-only">Search results</h2>
      ${content}
    </section>
  </div>`;
}
