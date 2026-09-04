import {registerGlobalInitFunc} from '../../modules/observer.ts';
import './deployments.css';

registerGlobalInitFunc('initDeploymentsMatrix', async (el: HTMLElement) => {
  const {initDeploymentsMatrix} = await import('./matrix.ts');
  await initDeploymentsMatrix(el);
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

// initDeploymentsReleaseBadges mounts on the fragment's own <script> element rather than a
// content div: it decorates release-list entries Gitea already rendered and owns no markup
// of its own. registerGlobalInitFunc works on any Element, a <script> included.
registerGlobalInitFunc('initDeploymentsReleaseBadges', async (el: HTMLElement) => {
  const {initDeploymentsReleaseBadges} = await import('./release-badges.ts');
  await initDeploymentsReleaseBadges(el);
});
