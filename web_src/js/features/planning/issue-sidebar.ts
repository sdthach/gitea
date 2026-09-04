import {createElementFromAttrs} from '../../utils/dom.ts';
import {getIssueFacets, getIssueTypeAssignments, sessionConfig, type PlanningApiConfig} from './api.ts';
import {formatDurationSeconds} from './duration.ts';
import type {Field, IssueFacets} from './types.ts';

function dateValue(unix: number): string {
  return unix ? new Date(unix * 1000).toISOString().slice(0, 10) : '';
}

// heading matches templates/repo/issue/sidebar/due_date.tmpl's own divider-plus-label shape.
function heading(text: string): HTMLElement[] {
  const divider = document.createElement('div');
  divider.className = 'divider';
  const strong = document.createElement('strong');
  strong.textContent = text;
  const span = createElementFromAttrs('span', {class: 'text'}, strong);
  return [divider, span];
}

function saveButton(label: string): HTMLElement {
  return createElementFromAttrs('button', {class: 'ui icon button tw-mt-1', type: 'submit'}, label);
}

// issueHref rewrites the issue page's own URL onto a different issue number in this repository.
function issueHref(number: number): string {
  return window.location.pathname.replace(/\/issues\/\d+$/, `/issues/${number}`);
}

function startSection(facets: IssueFacets, canWrite: boolean, postBase: string): HTMLElement[] {
  const value = document.createElement('div');
  value.textContent = facets.schedule.start_unix ?
    `${dateValue(facets.schedule.start_unix)} (${facets.schedule.start_source})` :
    'not set';
  const nodes = [...heading('Start'), value];
  if (!canWrite) return nodes;
  const input = createElementFromAttrs('input', {type: 'date', name: 'start', 'aria-label': 'Start date', value: dateValue(facets.schedule.start_unix)});
  nodes.push(createElementFromAttrs('form', {
    class: 'ui fluid action input form-fetch-action tw-mt-2', method: 'post', action: `${postBase}/issues/${facets.issue_id}/schedule`,
  }, input, saveButton('Save start')));
  return nodes;
}

// typeSection's badge fetches the same single-issue batch endpoint type-icons.ts uses for a list.
async function typeSection(facets: IssueFacets, canWrite: boolean, postBase: string, config: PlanningApiConfig): Promise<HTMLElement[]> {
  const value = document.createElement('div');
  value.className = 'flex-text-block';
  if (facets.type) {
    try {
      const [row] = await getIssueTypeAssignments(config, facets.repo_id, [facets.issue_id]);
      if (row) {
        const icon = document.createElement('span');
        icon.innerHTML = row.icon_svg;
        icon.style.color = row.color;
        value.append(icon);
      }
    } catch {
      // the name below still identifies the type even when the icon batch fails
    }
    const name = document.createElement('span');
    name.textContent = facets.type.name;
    value.append(name);
  } else {
    value.textContent = 'none';
  }

  const nodes = [...heading('Type'), value];
  if (!canWrite) return nodes;
  // A plain select, not "ui dropdown": Fomantic hides the native control behind its own
  // widget, which is a second thing this fragment would have to keep initialized correctly.
  const select = createElementFromAttrs('select', {name: 'type_id', 'aria-label': 'Type'});
  select.append(new Option('none', ''));
  for (const t of facets.types) select.append(new Option(t.name, String(t.id), false, facets.type?.type_id === t.id));
  nodes.push(createElementFromAttrs('form', {
    class: 'form-fetch-action tw-mt-2', method: 'post', action: `${postBase}/issues/${facets.issue_id}/type`,
  }, select, saveButton('Save type')));
  return nodes;
}

function parentSection(facets: IssueFacets, canWrite: boolean, postBase: string): HTMLElement[] {
  const value = document.createElement('div');
  value.className = 'flex-text-block';
  if (facets.parent) {
    const link = document.createElement('a');
    link.href = issueHref(facets.parent.number);
    link.textContent = `#${facets.parent.number} ${facets.parent.title}`;
    value.append(link);
    if (canWrite) {
      const clear = createElementFromAttrs('input', {type: 'hidden', name: 'parent', value: ''});
      const button = createElementFromAttrs('button', {class: 'ui icon tiny button tw-ml-1', type: 'submit', 'aria-label': 'Remove parent'}, '×');
      value.append(createElementFromAttrs('form', {
        class: 'form-fetch-action', method: 'post', action: `${postBase}/issues/${facets.issue_id}/parent`,
      }, clear, button));
    }
  } else {
    value.textContent = 'none';
  }

  const nodes = [...heading('Parent'), value];
  if (!canWrite) return nodes;
  const input = createElementFromAttrs('input', {type: 'text', name: 'parent', placeholder: '#N', 'aria-label': 'Parent issue'});
  nodes.push(createElementFromAttrs('form', {
    class: 'ui fluid action input form-fetch-action tw-mt-2', method: 'post', action: `${postBase}/issues/${facets.issue_id}/parent`,
  }, input, saveButton('Save parent')));
  return nodes;
}

function subIssuesSection(facets: IssueFacets): HTMLElement[] {
  const nodes = heading('Sub-issues');
  if (!facets.children.length) {
    const none = document.createElement('div');
    none.textContent = 'none';
    nodes.push(none);
    return nodes;
  }

  const percent = facets.progress.total ? Math.round((facets.progress.closed / facets.progress.total) * 100) : 0;
  const track = document.createElement('div');
  track.className = 'planning-sidebar-progress';
  const bar = document.createElement('div');
  bar.className = 'planning-sidebar-progress-bar';
  bar.style.width = `${percent}%`;
  track.append(bar);
  nodes.push(track);

  const summary = document.createElement('div');
  summary.className = 'tw-text-12';
  summary.textContent = `${facets.progress.closed} / ${facets.progress.total} closed`;
  nodes.push(summary);

  const list = document.createElement('ul');
  list.className = 'planning-sidebar-children';
  for (const child of facets.children) {
    const li = document.createElement('li');
    if (child.is_closed) li.classList.add('tw-line-through');
    const link = document.createElement('a');
    link.href = issueHref(child.number);
    link.textContent = `#${child.number} ${child.title}`;
    li.append(link);
    list.append(li);
  }
  nodes.push(list);
  return nodes;
}

function estimateSection(facets: IssueFacets, canWrite: boolean, postBase: string): HTMLElement[] {
  const value = document.createElement('div');
  value.textContent = facets.time_estimate ? formatDurationSeconds(facets.time_estimate) : 'not set';
  const nodes = [...heading('Estimate'), value];
  if (!canWrite) return nodes;
  const input = createElementFromAttrs('input', {
    type: 'text', name: 'time_estimate', placeholder: '4h30m', 'aria-label': 'Estimate',
    value: facets.time_estimate ? formatDurationSeconds(facets.time_estimate) : '',
  });
  nodes.push(createElementFromAttrs('form', {
    class: 'ui fluid action input form-fetch-action tw-mt-2', method: 'post', action: `${postBase}/issues/${facets.issue_id}/estimate`,
  }, input, saveButton('Save estimate')));
  return nodes;
}

// fieldString reads a field's value: a string, number or boolean by its kind's own contract.
function fieldString(value: unknown): string {
  if (typeof value === 'string') return value;
  if (typeof value === 'number' || typeof value === 'boolean') return String(value);
  return '';
}

function fieldValue(value: unknown): HTMLElement {
  const div = document.createElement('div');
  const text = fieldString(value);
  div.textContent = text === '' ? 'not set' : text;
  return div;
}

function fieldInput(field: Field, value: unknown): HTMLElement {
  const name = `field_${field.key}`;
  if (field.kind === 'select') {
    const select = createElementFromAttrs('select', {name, 'aria-label': field.label});
    select.append(new Option('—', ''));
    for (const option of field.options ?? []) select.append(new Option(option, option, false, value === option));
    return select;
  }
  const type = field.kind === 'int' ? 'number' : field.kind === 'date' ? 'date' : 'text';
  return createElementFromAttrs('input', {name, type, 'aria-label': field.label, value: fieldString(value)});
}

function fieldRow(field: Field, value: unknown, canWrite: boolean): HTMLElement {
  const label = document.createElement('label');
  label.textContent = field.label;
  return createElementFromAttrs('div', {class: 'field'}, label, canWrite ? fieldInput(field, value) : fieldValue(value));
}

// fieldsSection posts every field_<key> in one form to /fields as one partial update.
function fieldsSection(facets: IssueFacets, canWrite: boolean, postBase: string): HTMLElement[] {
  if (!facets.fields.length) return [];
  const rows = facets.fields.map((field) => fieldRow(field, facets.values[field.key], canWrite));
  if (!canWrite) return [...heading('Fields'), ...rows];
  const form = createElementFromAttrs('form', {
    class: 'ui form form-fetch-action tw-mt-2', method: 'post', action: `${postBase}/issues/${facets.issue_id}/fields`,
  }, ...rows, saveButton('Save fields'));
  return [...heading('Fields'), form];
}

function errorMessage(err: unknown): HTMLElement {
  const msg = document.createElement('div');
  msg.className = 'ui negative message tw-text-12';
  msg.textContent = `Could not load planning: ${err instanceof Error ? err.message : String(err)}`;
  return msg;
}

export async function initPlanningIssueSidebar(el: HTMLElement) {
  const issueId = Number(el.getAttribute('data-issue-id'));
  const canWrite = el.getAttribute('data-can-write') === 'true';
  const postBase = el.getAttribute('data-post-base')!;
  const config = sessionConfig();

  let facets: IssueFacets;
  try {
    facets = await getIssueFacets(config, issueId);
  } catch (err) {
    el.append(errorMessage(err));
    return;
  }

  el.append(
    ...startSection(facets, canWrite, postBase),
    ...(await typeSection(facets, canWrite, postBase, config)),
    ...parentSection(facets, canWrite, postBase),
    ...subIssuesSection(facets),
    ...estimateSection(facets, canWrite, postBase),
    ...fieldsSection(facets, canWrite, postBase),
  );
}
