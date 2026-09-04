import {approveReview, rejectReview} from './api.ts';
import type {DeploymentsApiConfig} from './api.ts';

const config: DeploymentsApiConfig = {apiBase: '/api/deployments/v1', appSubUrl: '', token: 'secret-page-token'};

afterEach(() => {
  vi.unstubAllGlobals();
});

// The review gate is the only thing that releases a held job, so a write that reaches it must
// carry the page's token: a read rides the browser session, but no write does.
test('approveReview carries the page token as an authorization header', async () => {
  const fetchMock = vi.fn().mockResolvedValue(Response.json({id: 1, state: 'approved'}, {status: 200}));
  vi.stubGlobal('fetch', fetchMock);

  await approveReview(config, 1);

  const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit];
  expect(url).toBe('/api/deployments/v1/reviews/1/approve');
  expect(new Headers(init.headers).get('authorization')).toBe('token secret-page-token');
});

test('rejectReview carries the page token as an authorization header', async () => {
  const fetchMock = vi.fn().mockResolvedValue(Response.json({id: 1, state: 'rejected'}, {status: 200}));
  vi.stubGlobal('fetch', fetchMock);

  await rejectReview(config, 1);

  const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit];
  expect(url).toBe('/api/deployments/v1/reviews/1/reject');
  expect(new Headers(init.headers).get('authorization')).toBe('token secret-page-token');
});
