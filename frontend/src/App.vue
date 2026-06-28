<script setup lang="ts">
// 根布局组件，与 v2 App.vue 结构一致，保留 NodeList 常驻左侧。
// 组件实现（NodeList/Bg）将在 P0 阶段从 v2 迁移并保留 scoped CSS。
import { RouterView } from 'vue-router'
// TODO(P0): 迁移 NodeList.vue 与 Bg.vue，保留 scoped 样式
</script>

<template>
  <notifications position="top right" />
  <div class="container scroll">
    <!-- <NodeList /> -->
    <RouterView v-auto-animate></RouterView>
  </div>
  <!-- <Bg /> -->
</template>

<style></style>
