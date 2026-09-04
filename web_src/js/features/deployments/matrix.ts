import {createApp} from 'vue';
import type {DeploymentsApiConfig} from './api.ts';

type MatrixPageData = {deploymentsMatrix: DeploymentsApiConfig};

export async function initDeploymentsMatrix(el: HTMLElement) {
  const config = (window.config.pageData as unknown as MatrixPageData).deploymentsMatrix;
  const {default: MatrixPage} = await import('./MatrixPage.vue');
  createApp(MatrixPage, {config}).mount(el);
}
