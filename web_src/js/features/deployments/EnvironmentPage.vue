<script lang="ts" setup>
import {computed, ref} from 'vue';
import {findUserByLogin, getOrgTeams, searchUserByUID} from './api.ts';
import {checks, normalize} from './environments.ts';
import type {DeploymentsApiConfig} from './api.ts';
import type {CheckKey} from './environments.ts';
import type {Environment, GiteaTeam} from './types.ts';

const props = defineProps<{
  config: DeploymentsApiConfig;
  env: Environment;
  canWrite: boolean;
  siblings: Environment[];
  ownerLogin: string;
}>();

const emit = defineEmits<{save: [draft: Environment]}>();

const editingKey = ref<CheckKey | null>(null);
const draft = ref<Environment | null>(null);
const teams = ref<GiteaTeam[]>([]);
const userNames = ref(new Map<number, string>());
const newUsername = ref('');
const addUserError = ref('');

const present = computed(() => checks.filter((c) => c.present(props.env)));
const missing = computed(() => checks.filter((c) => !c.present(props.env)));
// visible is present plus, appended, a check just added through the picker: it has no row in
// props.env yet (nothing is saved until Save is pressed), so present() alone would drop its
// editor from the page the instant it opens.
const visible = computed(() => {
  if (editingKey.value && !present.value.some((c) => c.key === editingKey.value)) {
    return [...present.value, checks.find((c) => c.key === editingKey.value)!];
  }
  return present.value;
});
const candidates = computed(() => props.siblings.filter((s) => s.name !== props.env.name));

async function resolveUserNames(ids: number[]) {
  for (const id of ids) {
    if (userNames.value.has(id)) continue;
    const found = await searchUserByUID(props.config, id);
    if (found?.data?.length) userNames.value.set(id, found.data[0].login);
  }
}

// beginEdit enters edit mode on built, which is either the environment's own current values
// (startEdit) or a freshly-added check's defaults (startAdd) — both need the same sequence
// default and the same lazy team/username lookups the bypass editor draws from.
async function beginEdit(key: CheckKey, built: Environment) {
  editingKey.value = key;
  draft.value = built;
  if (key === 'sequence' && !built.depends_on.length && candidates.value.length) {
    built.depends_on = [candidates.value[0].name];
  }
  if (key === 'bypass') {
    if (!teams.value.length) teams.value = await getOrgTeams(props.config, props.ownerLogin) ?? [];
    await resolveUserNames(built.reviewer_user_ids);
  }
}

function startEdit(key: CheckKey) {
  void beginEdit(key, normalize({...props.env}));
}

function startAdd(key: CheckKey) {
  const built = normalize({...props.env});
  checks.find((c) => c.key === key)!.add(built);
  void beginEdit(key, built);
}

function onAddCheckChange(event: Event) {
  const select = event.target as HTMLSelectElement;
  const value = select.value;
  select.value = '';
  if (value) startAdd(value as CheckKey);
}

function cancel() {
  editingKey.value = null;
  draft.value = null;
}

function removeCheck(key: CheckKey) {
  const built = normalize({...props.env});
  checks.find((c) => c.key === key)!.remove(built);
  emit('save', built);
}

function save() {
  if (!draft.value) return;
  emit('save', draft.value);
  editingKey.value = null;
  draft.value = null;
}

async function addBypassUser() {
  const username = newUsername.value.trim();
  if (!username || !draft.value) return;
  const user = await findUserByLogin(props.config, username);
  if (!user) {
    addUserError.value = `no user named ${username} is visible to you`;
    return;
  }
  addUserError.value = '';
  userNames.value.set(user.id, user.login);
  if (!draft.value.reviewer_user_ids.includes(user.id)) draft.value.reviewer_user_ids.push(user.id);
  newUsername.value = '';
}

function removeBypassUser(id: number) {
  if (!draft.value) return;
  draft.value.reviewer_user_ids = draft.value.reviewer_user_ids.filter((other) => other !== id);
}
</script>

<template>
  <div>
    <div class="ui form" id="deployments-add-check-form">
      <div v-if="canWrite && missing.length" class="inline fields">
        <div class="field">
          <label for="deployments-add-check">Add check</label>
          <select id="deployments-add-check" @change="onAddCheckChange">
            <option value="">Add check…</option>
            <option v-for="check in missing" :key="check.key" :value="check.key">{{ check.label }}</option>
          </select>
        </div>
      </div>
    </div>

    <div id="deployments-checks">
      <div v-for="check in visible" :key="check.key" class="ui segment" :data-check="check.key">
        <template v-if="editingKey === check.key && draft">
          <h4 class="ui header">{{ check.label }}</h4>

          <div v-if="check.key === 'reviews'" class="ui form">
            <div class="field">
              <label for="deployments-required-reviewers">Required reviewers</label>
              <input id="deployments-required-reviewers" v-model.number="draft.required_reviewers" type="number" min="1">
            </div>
            <div class="field">
              <label for="deployments-review-policy">Who may approve</label>
              <select id="deployments-review-policy" v-model="draft.review_policy">
                <option value="any_approver">anyone with write on Actions</option>
                <option value="others_only">anyone except whoever asked</option>
              </select>
            </div>
          </div>

          <div v-else-if="check.key === 'sequence'" class="ui form">
            <p v-if="!candidates.length" class="tw-text-14">
              No other environment shares this scope, so there is nothing a release could pass through first.
              Create a second environment in this scope, then add this check.
            </p>
            <template v-else>
              <div class="field">
                <label for="deployments-depends-on">Depends on</label>
                <select id="deployments-depends-on" v-model="draft.depends_on[0]">
                  <option v-for="sibling in candidates" :key="sibling.name" :value="sibling.name">{{ sibling.name }}</option>
                </select>
              </div>
              <div class="field">
                <label for="deployments-require-prior-deployment">
                  <input id="deployments-require-prior-deployment" v-model="draft.require_prior_deployment" type="checkbox">
                  refuse a deploy whose dependency has never held the release
                </label>
              </div>
            </template>
          </div>

          <div v-else-if="check.key === 'release_kind'" class="ui form">
            <div class="field">
              <label for="deployments-releases-only">
                <input id="deployments-releases-only" v-model="draft.releases_only" type="checkbox">
                refuse prereleases; this environment takes finished releases only
              </label>
            </div>
          </div>

          <div v-else-if="check.key === 'bypass'" class="ui form">
            <div class="field">
              <label>Users who may bypass</label>
              <div class="tw-flex tw-gap-2 tw-flex-wrap">
                <span v-for="id in draft.reviewer_user_ids" :key="id" class="ui label">
                  {{ userNames.get(id) ?? `user ${id}` }}
                  <a class="ui basic mini button" role="button" @click="removeBypassUser(id)">×</a>
                </span>
              </div>
            </div>
            <div class="inline fields">
              <div class="field">
                <label for="deployments-bypass-user">Add a user</label>
                <input id="deployments-bypass-user" v-model="newUsername" type="text" placeholder="username" @keydown.enter.prevent="addBypassUser">
              </div>
              <div class="field">
                <button type="button" class="ui basic button" @click="addBypassUser">Add user</button>
              </div>
            </div>
            <p v-if="addUserError" class="tw-text-12">{{ addUserError }}</p>
            <div v-if="teams.length" class="field">
              <label for="deployments-bypass-teams">Teams who may bypass</label>
              <select id="deployments-bypass-teams" v-model="draft.reviewer_team_ids" multiple>
                <option v-for="team in teams" :key="team.id" :value="team.id">{{ team.name }}</option>
              </select>
            </div>
            <div class="field">
              <label for="deployments-admins-can-bypass">
                <input id="deployments-admins-can-bypass" v-model="draft.admins_can_bypass" type="checkbox">
                a repository administrator may bypass this
              </label>
            </div>
          </div>

          <div class="tw-flex tw-gap-2 tw-mt-2">
            <button
              v-if="check.key !== 'sequence' || candidates.length"
              type="button" class="ui primary button" @click="save"
            >
              Save
            </button>
            <button type="button" class="ui basic button" @click="cancel">Cancel</button>
          </div>
        </template>
        <template v-else>
          <div class="tw-flex tw-items-center tw-justify-between">
            <strong>{{ check.label }}</strong>
            <div v-if="canWrite" class="tw-flex tw-gap-2">
              <button type="button" class="ui basic mini button" @click="startEdit(check.key)">Edit</button>
              <button type="button" class="ui basic mini button" @click="removeCheck(check.key)">Remove</button>
            </div>
          </div>
          <div class="tw-text-14">{{ check.summary(env) }}</div>
        </template>
      </div>
      <p v-if="!visible.length" class="tw-text-14">
        Nothing gates a deploy here. Anyone with write on Actions can deploy any release, in any order.
      </p>
    </div>
  </div>
</template>
