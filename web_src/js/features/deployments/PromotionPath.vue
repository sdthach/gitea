<script lang="ts" setup>
import {computed, onMounted, reactive, ref} from 'vue';
import {elbowPath} from '../planning/arrows.ts';
import type {ArrowRect} from '../planning/arrows.ts';
import {ApiError, getEnvironmentPaths, getEnvironments, updateEnvironment} from './api.ts';
import {normalize, payloadOf} from './environments.ts';
import ErrorBanner from './ErrorBanner.vue';
import type {DeploymentsApiConfig} from './api.ts';
import type {Environment, PathEdge, PathNode} from './types.ts';

const props = defineProps<{config: DeploymentsApiConfig; repoId: number; label: string}>();

const nodes = ref<PathNode[]>([]);
const edges = ref<PathEdge[]>([]);
const byName = reactive(new Map<string, Environment>());
const errorMessage = ref('');
const errorAction = ref('');
const dragFrom = ref('');

const nodeWidth = 168;
const nodeHeight = 76;
const gapX = 40;

const rects = computed<Map<string, ArrowRect>>(() => {
  const out = new Map<string, ArrowRect>();
  nodes.value.forEach((node, i) => {
    out.set(node.name, {x: i * (nodeWidth + gapX), y: 0, width: nodeWidth, height: nodeHeight});
  });
  return out;
});

const svgWidth = computed(() => Math.max(nodeWidth, nodes.value.length * (nodeWidth + gapX) - gapX));

const edgePaths = computed(() => {
  const out: Array<{edge: PathEdge; path: string}> = [];
  for (const edge of edges.value) {
    const from = rects.value.get(edge.from);
    const to = rects.value.get(edge.to);
    if (from && to) out.push({edge, path: elbowPath(from, to)});
  }
  return out;
});

function fail(err: unknown, fallback: string) {
  if (err instanceof ApiError) {
    errorMessage.value = err.message;
    errorAction.value = err.suggestedAction || fallback;
  } else {
    errorMessage.value = String(err);
    errorAction.value = fallback;
  }
}

async function load() {
  try {
    const [path, own, defaults] = await Promise.all([
      getEnvironmentPaths(props.config, props.repoId),
      getEnvironments(props.config, {repoId: props.repoId}),
      props.repoId ? getEnvironments(props.config, {repoId: 0}) : Promise.resolve([]),
    ]);
    byName.clear();
    for (const env of defaults) byName.set(env.name, normalize(env));
    for (const env of own) byName.set(env.name, normalize(env)); // a repo's own row shadows the default of the same name
    // required_status_contexts arrives null, not [], for a node that never set one — the same
    // gap normalize() closes for an Environment row.
    nodes.value = path.nodes.map((node) => ({
      ...node,
      checks: {...node.checks, required_status_contexts: node.checks.required_status_contexts ?? []},
    }));
    edges.value = path.edges;
    errorMessage.value = '';
  } catch (err) {
    fail(err, 'Retry, and check the server log if it keeps failing.');
  }
}

// canWrite requires can_write and that the row belongs to this page's own scope: an inherited
// default (byName holds it at repo_id 0) must never be written by PUT /environments/{id} on
// the instance-wide row just because a repository view renders it alongside the repo's own.
function canWrite(name: string): boolean {
  const env = byName.get(name);
  return !!env && env.can_write && env.repo_id === props.repoId;
}

async function persist(name: string, mutate: (draft: Environment) => void, revert: () => void) {
  const env = byName.get(name);
  if (!env) return;
  const before = {...env};
  mutate(env);
  try {
    await updateEnvironment(props.config, env.id, payloadOf(env));
    errorMessage.value = '';
  } catch (err) {
    byName.set(name, before);
    revert();
    fail(err, 'Retry, and check the server log if it keeps failing.');
  }
}

function connect(fromName: string, toName: string) {
  if (fromName === toName || !canWrite(fromName)) return;
  const source = byName.get(fromName);
  if (!source || source.depends_on.includes(toName)) return;
  const previousEdges = edges.value;
  edges.value = [...edges.value, {from: toName, to: fromName}];
  persist(fromName, (draft) => {
    draft.depends_on = [...draft.depends_on, toName];
  }, () => {
    edges.value = previousEdges;
  });
}

function removeEdge(edge: PathEdge) {
  if (!canWrite(edge.to)) return;
  const previousEdges = edges.value;
  edges.value = edges.value.filter((e) => !(e.from === edge.from && e.to === edge.to));
  persist(edge.to, (draft) => {
    draft.depends_on = draft.depends_on.filter((d) => d !== edge.from);
  }, () => {
    edges.value = previousEdges;
  });
}

function toggleAutoPromote(node: PathNode) {
  if (!canWrite(node.name)) return;
  node.auto_promote = !node.auto_promote;
  persist(node.name, (draft) => {
    draft.auto_promote = node.auto_promote;
  }, () => {
    node.auto_promote = !node.auto_promote;
  });
}

function toggleExclusiveLock(node: PathNode) {
  if (!canWrite(node.name)) return;
  node.checks.exclusive_lock = !node.checks.exclusive_lock;
  persist(node.name, (draft) => {
    draft.exclusive_lock = node.checks.exclusive_lock;
  }, () => {
    node.checks.exclusive_lock = !node.checks.exclusive_lock;
  });
}

function setWaitMinutes(node: PathNode, value: string) {
  if (!canWrite(node.name)) return;
  const previous = node.checks.wait_minutes;
  const minutes = Math.max(0, Number(value) || 0);
  node.checks.wait_minutes = minutes;
  persist(node.name, (draft) => {
    draft.wait_minutes = minutes;
  }, () => {
    node.checks.wait_minutes = previous;
  });
}

function setRequiredStatusContexts(node: PathNode, value: string) {
  if (!canWrite(node.name)) return;
  const previous = node.checks.required_status_contexts;
  const contexts = value.split(',').map((c) => c.trim()).filter((c) => c !== '');
  node.checks.required_status_contexts = contexts;
  persist(node.name, (draft) => {
    draft.required_status_contexts = contexts;
  }, () => {
    node.checks.required_status_contexts = previous;
  });
}

function startDrag(name: string) {
  if (!canWrite(name)) return;
  dragFrom.value = name;
  const onUp = (event: MouseEvent) => {
    window.removeEventListener('mouseup', onUp);
    const target = (event.target as HTMLElement)?.closest('[data-promotion-node]') as HTMLElement | null;
    const toName = target?.dataset.promotionNode;
    if (toName) connect(dragFrom.value, toName);
    dragFrom.value = '';
  };
  window.addEventListener('mouseup', onUp);
}

onMounted(load);
</script>

<template>
  <div class="deployments-promotion-path" :data-repo-id="repoId">
    <h4 class="ui header">{{ label }}</h4>
    <ErrorBanner header="Could not update the promotion path" :message="errorMessage" :suggested-action="errorAction"/>
    <div class="deployments-promotion-canvas" :style="{width: `${svgWidth}px`, height: `${nodeHeight}px`}">
      <svg class="deployments-promotion-edges" :width="svgWidth" :height="nodeHeight">
        <path
          v-for="{edge, path} in edgePaths" :key="`${edge.from}>${edge.to}`"
          :d="path" class="deployments-promotion-edge" @click="removeEdge(edge)"
        />
      </svg>
      <div
        v-for="(node, i) in nodes" :key="node.name" :data-promotion-node="node.name"
        class="deployments-promotion-node" :class="{'deployments-promotion-node-locked': !canWrite(node.name)}"
        :style="{left: `${i * (nodeWidth + gapX)}px`, width: `${nodeWidth}px`, height: `${nodeHeight}px`}"
        @mousedown="startDrag(node.name)"
      >
        <strong>{{ node.name }}</strong>
        <label class="tw-text-12">
          <input
            type="checkbox" :checked="node.auto_promote" :disabled="!canWrite(node.name)"
            @click.stop @change="toggleAutoPromote(node)"
          > auto-promote
        </label>
      </div>
    </div>
    <table class="ui very basic compact table deployments-promotion-checks">
      <thead><tr><th>Environment</th><th>Wait minutes</th><th>Exclusive lock</th><th>Required status contexts</th></tr></thead>
      <tbody>
        <tr v-for="node in nodes" :key="node.name">
          <td>{{ node.name }}</td>
          <td>
            <input
              type="number" min="0" class="tw-w-full" :value="node.checks.wait_minutes"
              :disabled="!canWrite(node.name)" @change="setWaitMinutes(node, ($event.target as HTMLInputElement).value)"
            >
          </td>
          <td>
            <input
              type="checkbox" :checked="node.checks.exclusive_lock" :disabled="!canWrite(node.name)"
              @change="toggleExclusiveLock(node)"
            >
          </td>
          <td>
            <input
              type="text" class="tw-w-full" :value="node.checks.required_status_contexts.join(', ')"
              :disabled="!canWrite(node.name)" placeholder="ci/build, ci/test"
              @change="setRequiredStatusContexts(node, ($event.target as HTMLInputElement).value)"
            >
          </td>
        </tr>
      </tbody>
    </table>
    <p class="tw-text-12">Drag one environment onto another to make it depend on it; click an edge to remove it.</p>
  </div>
</template>
