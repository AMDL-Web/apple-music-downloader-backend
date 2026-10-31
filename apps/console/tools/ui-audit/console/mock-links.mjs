// The mock server's link answers: lookup and quality, from the fixed
// catalog in fixtures.mjs.

import { CATALOG, LOSSY_ALBUM, findAlbum } from './fixtures.mjs';
import { json, apiError, readBody, parseLink } from './mock-http.mjs';

export async function lookup(req, res) {
  const body = (await readBody(req)) || {};
  const link = parseLink(body.url);
  if (!link) return apiError(res, 400, 'invalid_url', 'Not an Apple Music link.');
  const base = { input: body.url, type: link.type, storefront: link.storefront, id: link.id, ...(link.track_id ? { track_id: link.track_id } : {}) };
  if (link.id === '1234567890') return apiError(res, 404, 'catalog_not_found', `read Apple Music catalog: Apple Music has no ${link.type} ${link.id} in storefront ${link.storefront} (404)`, { link: base });
  const a = link.id === LOSSY_ALBUM.id ? LOSSY_ALBUM : findAlbum(link.id);
  const song = CATALOG.songs.find((s) => s.id === link.id || s.id === link.track_id);
  const artist = CATALOG.artists.find((x) => x.id === link.id);
  const src = a || song || artist || CATALOG.albums[[...link.id].reduce((n, c) => n + c.charCodeAt(0), 0) % CATALOG.albums.length];
  const out = { ...base, title: src.name, artwork_url: src.artwork_url };
  for (const k of ['artwork_bg_color', 'artwork_text_color1', 'artwork_text_color2', 'artwork_text_color3', 'artwork_text_color4', 'release_date', 'genre']) if (src[k]) out[k] = src[k];
  if (link.type !== 'playlist' && link.type !== 'station') out.artist_name = src.artist_name || src.name;
  if (link.type === 'album') out.track_count = src.track_count || 10;
  if (link.type === 'song') out.album_name = src.album_name || src.name;
  if (link.type === 'playlist') out.curator_name = 'Apple Music';
  if (src.duration_ms) out.duration_ms = src.duration_ms;
  if (src.audio_traits) out.audio_traits = src.audio_traits;
  return json(res, 200, out);
}

export async function quality(req, res) {
  const body = (await readBody(req)) || {};
  const link = parseLink(body.url);
  if (!link) return apiError(res, 400, 'invalid_url', 'Not an Apple Music link.');
  const lossy = link.id === LOSSY_ALBUM.id;
  const a = lossy ? LOSSY_ALBUM : findAlbum(link.id) || CATALOG.albums[0];
  const n = link.type === 'song' ? 1 : Math.min(a.track_count || 10, 30);
  // A release without Enhanced HLS: every quality unavailable, as the server answers.
  const opt = (id, label, extra, available = true) => ({ id, label, available: available && !lossy, ...(available && !lossy ? extra : {}) });
  const tracks = [];
  for (let i = 0; i < n; i++) {
    const s = CATALOG.songs[i % CATALOG.songs.length];
    const hi = i % 5 === 0;
    tracks.push({
      song: { id: String(3000000000 + i), name: s.name, artist: a.artist_name, album: a.name, has_lyrics: true },
      qualities: [
        opt('aac', 'AAC', { codec: 'aac', codec_id: 'audio-stereo-256', channels: 2, bitrate: 256000, sample_rate: 44100 }),
        opt('aac-binaural', 'AAC Binaural', {}, false),
        opt('aac-downmix', 'AAC Downmix', {}, false),
        opt('alac', 'Lossless (ALAC)', { codec: 'alac', codec_id: `audio-alac-stereo-${hi ? 96000 : 48000}-24`, channels: 2, bit_depth: 24, sample_rate: hi ? 96000 : 48000, bitrate: hi ? 2565559 : 1877679 }),
        opt('ec3', 'Dolby Atmos', { codec: 'ec3', codec_id: 'audio-atmos-2768', channels: 16, bitrate: 768000, sample_rate: 48000 }, i % 3 !== 0),
        opt('ac3', 'Dolby Digital', {}, false),
      ],
    });
  }
  return json(res, 200, { input: body.url, storefront: link.storefront, type: link.type, adam_id: link.id, tracks });
}
