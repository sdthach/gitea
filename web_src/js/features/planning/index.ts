import {registerGlobalInitFunc} from '../../modules/observer.ts';
import './planning.css';

registerGlobalInitFunc('initPlanningProject', async (el: HTMLElement) => {
  const {initPlanningProject} = await import('./project.ts');
  await initPlanningProject(el);
});

registerGlobalInitFunc('initPlanningIssueSidebar', async (el: HTMLElement) => {
  const {initPlanningIssueSidebar} = await import('./issue-sidebar.ts');
  await initPlanningIssueSidebar(el);
});

registerGlobalInitFunc('initPlanningTypeIcon', async (el: HTMLElement) => {
  const {initPlanningTypeIcon} = await import('./type-icons.ts');
  await initPlanningTypeIcon(el);
});

registerGlobalInitFunc('initPlanningMilestoneStart', async (el: HTMLElement) => {
  const {initPlanningMilestoneStart} = await import('./milestone-start.ts');
  await initPlanningMilestoneStart(el);
});
