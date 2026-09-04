<script lang="ts" setup>
import {computed} from 'vue';
import SvgIcon from '../../../components/SvgIcon.vue';
import type {SvgName} from '../../../svg.ts';
import {contrastColor} from '../../../utils/color.ts';
import type {Card, LabelRef} from '../types.ts';
import AvatarImg from './AvatarImg.vue';

const props = defineProps<{card: Card; labels: LabelRef[]; canEditIssues: boolean}>();

const labelsByName = computed(() => new Map(props.labels.map((label) => [label.name, label])));

function labelColor(name: string): string {
  return labelsByName.value.get(name)?.color ?? '#cccccc';
}

const avatarByLogin = computed(() => new Map(props.card.assignee_avatars.map((a) => [a.login, a.avatar_url])));

</script>

<template>
  <div class="board-card ui card tw-p-2" :data-issue-id="card.issue_id">
    <div class="tw-flex tw-items-center tw-gap-1">
      <span v-if="canEditIssues" data-drag class="tw-cursor-grab tw-text-text-light" aria-hidden="true">
        <svg-icon name="octicon-grabber"/>
      </span>
      <svg-icon v-if="card.type_icon" :name="(card.type_icon as SvgName)" :style="{color: card.type_color}"/>
      <a :href="card.url" class="tw-flex-1 tw-truncate">{{ card.title }}</a>
      <span class="tw-text-text-light">#{{ card.number }}</span>
    </div>

    <div v-if="card.labels.length" class="tw-flex tw-flex-wrap tw-gap-1 tw-mt-1">
      <span
        v-for="name in card.labels" :key="name" class="ui label tw-text-xs"
        :style="{backgroundColor: labelColor(name), color: contrastColor(labelColor(name))}"
      >{{ name }}</span>
    </div>

    <div class="tw-flex tw-items-center tw-justify-between tw-gap-2 tw-mt-1 tw-text-text-light tw-text-xs">
      <div class="tw-flex tw-items-center tw-gap-1">
        <AvatarImg v-for="login in card.assignees" :key="login" :login="login" :size="20" :avatar-url="avatarByLogin.get(login)"/>
      </div>
      <span v-if="card.milestone" class="tw-truncate">{{ card.milestone }}</span>
      <span v-if="card.points">{{ card.points }}pt</span>
    </div>
  </div>
</template>
