import {createApp} from 'vue';

export type PlanningProjectConfig = {
  apiBase: string;
  token: string;
  repoId: number;
  repoFullName: string;
  projectId: number;
};

type PlanningPageData = {planningProject: PlanningProjectConfig};

export async function initPlanningProject(el: HTMLElement) {
  const config = (window.config.pageData as unknown as PlanningPageData).planningProject;
  const {default: ProjectPage} = await import('./components/ProjectPage.vue');
  createApp(ProjectPage, {config}).mount(el);
}
