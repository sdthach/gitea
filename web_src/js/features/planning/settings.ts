import {createApp} from 'vue';
import type {PlanningSettingsConfig} from './types.ts';

export type {PlanningSettingsConfig} from './types.ts';

type PlanningPageData = {planningSettings: PlanningSettingsConfig};

export async function initPlanningSettings(el: HTMLElement) {
  const config = (window.config.pageData as unknown as PlanningPageData).planningSettings;
  const {default: SettingsPage} = await import('./components/settings/SettingsPage.vue');
  createApp(SettingsPage, {config}).mount(el);
}
