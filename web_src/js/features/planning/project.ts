import {createApp} from 'vue';
import type {PlanningProjectConfig} from './types.ts';

export type {PlanningProjectConfig} from './types.ts';

type PlanningPageData = {planningProject: PlanningProjectConfig};

export async function initPlanningProject(el: HTMLElement) {
  const config = (window.config.pageData as unknown as PlanningPageData).planningProject;
  const {default: ProjectPage} = await import('./components/ProjectPage.vue');
  createApp(ProjectPage, {config}).mount(el);
}
