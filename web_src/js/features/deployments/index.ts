import {registerGlobalInitFunc} from '../../modules/observer.ts';
import './deployments.css';

registerGlobalInitFunc('initDeploymentsMatrix', async (el: HTMLElement) => {
  const {initDeploymentsMatrix} = await import('./matrix.ts');
  await initDeploymentsMatrix(el);
});

registerGlobalInitFunc('initDeploymentsEnvironments', async (el: HTMLElement) => {
  const {initDeploymentsEnvironments} = await import('./environments.ts');
  await initDeploymentsEnvironments(el);
});

registerGlobalInitFunc('initDeploymentsEnvironmentEdit', async (el: HTMLElement) => {
  const {initDeploymentsEnvironmentEdit} = await import('./environments.ts');
  await initDeploymentsEnvironmentEdit(el);
});

registerGlobalInitFunc('initDeploymentsNew', async (el: HTMLElement) => {
  const {initDeploymentsNew} = await import('./new.ts');
  await initDeploymentsNew(el);
});

registerGlobalInitFunc('initDeploymentsReviews', async (el: HTMLElement) => {
  const {initDeploymentsReviews} = await import('./reviews.ts');
  await initDeploymentsReviews(el);
});

registerGlobalInitFunc('initDeploymentsInsights', async (el: HTMLElement) => {
  const {initDeploymentsInsights} = await import('./insights.ts');
  await initDeploymentsInsights(el);
});

// initDeploymentsReleaseBadges mounts on the fragment's own <div>, not a page of its own: it decorates release-list entries Gitea already rendered.
registerGlobalInitFunc('initDeploymentsReleaseBadges', async (el: HTMLElement) => {
  const {initDeploymentsReleaseBadges} = await import('./release-badges.ts');
  await initDeploymentsReleaseBadges(el);
});
