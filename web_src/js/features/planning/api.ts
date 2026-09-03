import {request} from '../../modules/fetch.ts';

export type PlanningApiConfig = {
  apiBase: string;
  token: string;
};

// paths is every operation path the planning page fetches, spelled exactly as the published
// document spells them, so a page names the endpoint it is a client of rather than a
// rewritten copy of it.
export const paths = {
  board: '/board?',
};

export class ApiError extends Error {
  status: number;
  suggestedAction: string;

  constructor(message: string, status: number, suggestedAction: string) {
    super(message);
    this.name = 'ApiError';
    this.status = status;
    this.suggestedAction = suggestedAction;
  }
}

type CallOptions = {
  method?: string;
  body?: Record<string, unknown>;
};

export async function call<T>(config: PlanningApiConfig, path: string, {method = 'GET', body}: CallOptions = {}): Promise<T> {
  const headers: Record<string, string> = {accept: 'application/json'};
  if (method !== 'GET' && config.token) headers.authorization = `token ${config.token}`;

  const resp = await request(`${config.apiBase}${path}`, {
    method,
    headers,
    credentials: 'same-origin',
    ...(body !== undefined && {data: body}),
  });

  const text = await resp.text();
  let data: Record<string, unknown>;
  try {
    data = text ? JSON.parse(text) : {};
  } catch {
    data = {message: `the API returned ${resp.status}`};
  }

  if (!resp.ok) {
    throw new ApiError(
      typeof data.message === 'string' ? data.message : `the API returned ${resp.status}`,
      resp.status,
      typeof data.suggested_action === 'string' ? data.suggested_action : '',
    );
  }
  return data as T;
}
