import {registerGlobalInitFunc} from '../../modules/observer.ts';
import './planning.css';

registerGlobalInitFunc('initPlanningProject', async (el: HTMLElement) => {
  const {initPlanningProject} = await import('./project.ts');
  await initPlanningProject(el);
});
