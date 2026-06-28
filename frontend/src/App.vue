<script setup lang="ts">
// 根布局组件，与 v2 App.vue 结构一致，保留 NodeList 常驻左侧。
import { RouterView } from 'vue-router'
import NodeList from '@/components/NodeList.vue'
import Bg from '@/components/Bg.vue'
</script>

<template>
  <notifications position="top right" />
  <div class="container scroll">
    <NodeList />
    <RouterView v-auto-animate></RouterView>
  </div>
  <Bg />
</template>

<style></style>
