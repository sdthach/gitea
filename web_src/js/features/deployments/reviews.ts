import {createApp} from 'vue';
import type {ReviewsPageConfig} from './types.ts';

type ReviewsPageDataRoot = {deploymentsReviews: ReviewsPageConfig};

export async function initDeploymentsReviews(el: HTMLElement) {
  const config = (window.config.pageData as unknown as ReviewsPageDataRoot).deploymentsReviews;
  const {default: ReviewsPage} = await import('./ReviewsPage.vue');
  createApp(ReviewsPage, {config}).mount(el);
}
