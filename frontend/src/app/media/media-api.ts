import { Injectable } from '@angular/core';

/**
 * Same-origin API base, relative to <base href> so it follows the deploy prefix (/hypertube/api):
 * proxied to the Go backend by `ng serve` (proxy.conf.json) and in production.
 */
const API_BASE = 'api';

export const DEFAULT_PROVIDER = 'archive.org';

/** A search/popular result. The backend returns `Media[]`; older payloads used the archive.org envelope. */
export interface MediaDoc {
  id?: string;
  identifier?: string;
  external_id?: string;
  title?: string;
  format?: string;
  date?: string;
  thumbnailUrl?: string;
  thumbnail_url?: string;
  provider?: string;
  /** Production year ("" if unknown): the provider's, else OMDb's. */
  year?: string;
  /** IMDb rating from OMDb; 0 or absent when unknown. */
  imdb_rating?: number;
  genres?: string[] | null;
  imdb_id?: string;
  /** True when the logged-in user has already played this item. */
  watched?: boolean;
}

export interface MediaDetails {
  id?: string;
  external_id?: string;
  provider?: string;
  title?: string;
  description?: string;
  creator?: string;
  date?: string;
  duration_seconds?: number;
  imdb_rating?: number;
}

export interface MediaPlayResponse {
  internal_id?: string;
  status?: 'available' | 'ready' | 'downloading';
  url?: string;
  message?: string;
  /**
   * True when the backend converts the file on the fly (mkv, avi, ...) to fragmented MP4.
   * Such streams are not byte-range seekable: the native scrubber only works inside the
   * buffered part. To jump elsewhere, reload the url with `?t=<seconds>` (the new stream
   * then starts at that offset with currentTime 0). Once the download finishes the server
   * caches a regular .mp4 and later plays report transcoded=false (fully seekable).
   */
  transcoded?: boolean;
}

interface ArchiveEnvelope {
  response?: { docs?: MediaDoc[] };
}

export interface MediaListParams {
  page: number;
  query: string;
  rows: number;
}

/** Thin client for the media endpoints served by the Go backend under /api/media. */
@Injectable({ providedIn: 'root' })
export class MediaApi {
  listUrl({ page, query, rows }: MediaListParams): string {
    const search = query.length > 0;
    const params = new URLSearchParams({ page: String(page), rows: String(rows) });
    if (search) {
      params.set('q', query);
    }
    return `${API_BASE}/media${search ? '/search' : ''}?${params}`;
  }

  async list(params: MediaListParams, signal?: AbortSignal): Promise<MediaDoc[]> {
    const response = await fetch(this.listUrl(params), { signal });
    if (!response.ok) {
      throw new Error(`Media API returned ${response.status}`);
    }
    return extractDocs(await response.json());
  }

  async details(externalId: string, provider: string, signal?: AbortSignal): Promise<MediaDetails> {
    const params = new URLSearchParams({ external_id: externalId, provider });
    const response = await fetch(`${API_BASE}/media/details?${params}`, { signal });
    if (!response.ok) {
      throw new Error(`Media details request failed with ${response.status}`);
    }
    return (await response.json()) as MediaDetails;
  }

  /** Starts (or reuses) the server-side download; resolves to the playable URL or null. */
  async play(externalId: string, provider: string): Promise<string | null> {
    const params = new URLSearchParams({ external_id: externalId, provider });
    const response = await fetch(`${API_BASE}/media/play?${params}`).catch(() => null);
    if (response?.ok !== true) {
      return null;
    }
    const payload = (await response.json()) as MediaPlayResponse;
    // The backend answers root-relative ("/stream/files/..."): resolve it against <base href> instead.
    const url = payload.url?.trim().replace(/^\/+/, '') ?? '';
    return url.length > 0 ? url : null;
  }
}

function extractDocs(payload: unknown): MediaDoc[] {
  if (Array.isArray(payload)) {
    return payload as MediaDoc[];
  }
  if (payload !== null && typeof payload === 'object') {
    return (payload as ArchiveEnvelope).response?.docs ?? [];
  }
  return [];
}
