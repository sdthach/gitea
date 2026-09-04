import {createApp} from 'vue';
import type {InsightsPageConfig} from './types.ts';

type InsightsPageDataRoot = {deploymentsInsights: InsightsPageConfig};

export async function initDeploymentsInsights(el: HTMLElement) {
  const config = (window.config.pageData as unknown as InsightsPageDataRoot).deploymentsInsights;
  const {default: InsightsPage} = await import('./InsightsPage.vue');
  createApp(InsightsPage, {config}).mount(el);
}
