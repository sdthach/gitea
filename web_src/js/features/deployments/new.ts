import {createApp} from 'vue';
import type {DeploymentsApiConfig} from './api.ts';

type NewPageData = {deploymentsNew: DeploymentsApiConfig};

export async function initDeploymentsNew(el: HTMLElement) {
  const config = (window.config.pageData as unknown as NewPageData).deploymentsNew;
  const {default: NewPage} = await import('./NewPage.vue');
  createApp(NewPage, {config}).mount(el);
}
