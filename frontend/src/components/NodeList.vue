<template>
    <Finder v-if="finderNode" :node="finderNode" @close="finderNode = null" />
    <AddNode v-if="showAddNodeModal" :node="newNode" @close="showAddNodeModal = false" @add-node="handleAddNode" />
    <button class="sidebar-toggle" :title="collapsed ? t('sidebar.expand') : t('sidebar.collapse')" @click="toggle">
        <IconPanelToggle :collapsed="collapsed" />
    </button>
    <div class="node-list-container" :class="{ collapsed }">
        <div class="node-list">
            <div class="bar">
                <span class="header-title" v-if="nodesStore.selectedNodes.length <= 0">{{ t("header.total") }} {{
                    nodesLength }} {{ t("header.nodes") }}</span>
                <span class="header-title" v-if="nodesStore.selectedNodes.length > 0">{{ t("header.choosed") }} {{
                    nodesStore.selectedNodes.length }} {{ t("header.nodes") }}</span>
                <div class="buttons">
                    <button class="small-button" @click="selectAll" v-show="!isSelectAll && nodesLength > 0">{{
                        t("header.actions.all") }}</button>
                    <button class="small-button" @click="deselectAll" v-show="isSelectAll && nodesLength > 0">{{
                        t("header.actions.none") }}</button>
                    <button class="small-button" @click="showAddNodeModal = true">{{ t("header.actions.add") }}</button>
                </div>
            </div>

            <ul v-auto-animate class="nodes" v-if="nodesLength > 0">
                <li :class="{ 'node': 1, 'ssh-active': currentNode ? generateNodeId(currentNode) == generateNodeId(node) : false }"
                    v-for="node in Object.values(nodesStore.nodes).sort((a, b) => a.name > b.name ? 1 : -1)"
                    :key="generateNodeId(node)" @click="toggleNodeSelection(node)">
                    <div class="node-info">
                        <input type="checkbox" :checked="nodesStore.selectedNodes.includes(generateNodeId(node))" />
                        <div class="node-info-name" :title="`${node.name}[${node.host}:${node.port}]`"
                            :class="{ selected: nodesStore.selectedNodes.includes(generateNodeId(node)) }">
                            {{ node.name }}
                        </div>
                    </div>
                    <div class="buttons node-actions">
                        <IconFinder title="文件管理" @click.stop="openFinder(node)" />
                        <IconTerm title="终端" @click.stop="connectNode(node)" />
                        <IconEdit title="编辑节点" @click.stop="editNode(node)" />
                        <IconDelete title="删除节点" @click.stop="confirmDelete(node)" />
                    </div>
                </li>
            </ul>

            <div class="bar ">

                <span class="footer-title">
                    @Managi
                </span>
                <div class="buttons ">
                    <!-- <select value="" class="small-button" >
                        <option :disabled="true" value="">Language</option>
                        <option value="en">English</option>
                        <option value="zh">中文</option>
                    </select> -->

                    <button class="language-btn small-button" @click="handleLangChange">
                        <svg t="1782719021282" class="icon" viewBox="0 0 1024 1024" version="1.1" xmlns="http://www.w3.org/2000/svg" p-id="10665" width="128" height="128"><path d="M785.1008 297.8816V121.4464L531.456 208.64l253.6448 89.2416zM499.3024 197.632L232.5504 102.4v184.1664l266.752-88.9344zM334.1824 337.408l44.4416-12.7488v53.9648l95.2832-34.9184v171.52l-38.0928 12.6464v-25.344l-63.488 25.344 0.256 95.4368-38.4 12.544-0.3072-94.4128-63.1808 24.576v19.0464l-38.144 12.6976V426.2912l101.632-31.744V337.3568z m101.632 60.3136l-57.1904 22.2208v63.488l57.1904-19.0464V397.7216z m-165.12 123.8016l63.488-22.2208V432.64l-63.488 22.2208v66.6624z m247.6544-308.3776L175.4112 331.008v470.016l342.9376-114.3808V213.1456z m9.5744 489.4208c-123.392 41.2672-248.2688 82.5344-371.5584 123.8016V318.3104L804.1472 102.4v202.1888l63.488 20.0704v489.0112l-339.712-111.104z m266.6496-20.1728l-44.9024-162.56-45.2608-164.1472-48.8448-18.944-44.6976 131.1744-45.056 132.3008 49.664 17.8688 19.2512-60.5696 91.4432 33.4848 19.5072 73.8304 48.896 17.5616z m-83.2512-146.5344c-10.5472-40.2944-21.1456-80.5888-31.5904-120.8832l-30.9248 97.6896 62.464 23.1936z m-30.5664 312.1152c-121.1904 75.776-241.6128 85.1968-365.3632 6.5024l-6.656 10.3424c118.4768 75.264 236.3392 74.0864 357.6832 6.0416 7.1168-4.096 14.08-8.192 20.992-12.544l14.0288 21.9136 31.8976-58.5728-66.56 4.4544 13.9776 21.8624z" fill="#82AAFC" p-id="10666"></path></svg>
                    </button>

                    <button class="small-button" v-show="nodesLength > 0" @click="exploreNodes">{{
                        t("footer.actions.export") }}</button>
                    <button class="small-button" @click="importNodes">{{ t("footer.actions.import") }}</button>
                </div>
            </div>
        </div>
    </div>

</template>

<script setup lang="ts">
import { ref, computed, watch, onBeforeMount } from 'vue';
import { useRouter } from 'vue-router';
import AddNode from '@/components/AddNode.vue';
import IconDelete from '@/components/icons/IconDelete.vue'
import IconEdit from '@/components/icons/IconEdit.vue'
import IconTerm from '@/components/icons/IconTerm.vue'
import IconFinder from '@/components/icons/IconFinder.vue'
import IconPanelToggle from '@/components/icons/IconPanelToggle.vue'
import Finder from '@/components/Finder.vue'
import { useSidebar } from '@/composables/useSidebar'


import { useI18n } from 'vue-i18n'
import { handleError, handleMsg } from "@/helper";
import { setCachedNodes, getCachedNodes, oldApiNodeConvert } from '@/api';
import type { ApiNode, OldApiNode, AppConfig, ShortcutItem } from '@/protocol/types';
import { generateNodeId } from '@/protocol/types';
import { useNodesStore } from '@/stores/nodesStore';
import { useShortcutsStore } from '@/stores/shortcutsStore';


const router = useRouter();
const nodesStore = useNodesStore();
const shortcutsStore = useShortcutsStore();
const { collapsed, toggle } = useSidebar();
const finderNode = ref<ApiNode | null>(null);

const { t, locale } = useI18n()


const setLanguage = (lang: string) => locale.value = lang
const nodesLength = computed(() => Object.keys(nodesStore.nodes).length);
const showAddNodeModal = ref(false);


const handleLangChange = () => {
    const nextLang = locale.value === 'en' ? 'zh' : 'en'
    setLanguage(nextLang)
    localStorage.setItem('lang', nextLang)
}


onBeforeMount(() => {
    nodesStore.setAllNodes(getCachedNodes())
    const lang = localStorage.getItem('lang')
    if (lang) {
        setLanguage(lang)
    }
})


const newNode = ref<ApiNode>({
    name: '',
    host: '',
    port: 22,
    username: '',
    auth_type: 'password' as const,
    auth_value: ''
});

const isSelectAll = computed(() => {
    if (nodesStore.nodes) {
        return Object.keys(nodesStore.nodes).length === nodesStore.selectedNodes.length;
    }
    return false;
});
const selectAll = () => {
    nodesStore.selectAllNodes();
};

const deselectAll = () => {
    nodesStore.clearSelectedNodes()
};

// N2 修正：v3 nodesStore 的 remove/addFromSelected 接收 id: string，v2 传 node 对象。
// 改为传 generateNodeId(node)，与 v3 store 签名对齐。
const toggleNodeSelection = (node: ApiNode) => {
    if (nodesStore.selectedNodes.includes(generateNodeId(node))) {
        nodesStore.removeFromSelectedNodes(generateNodeId(node))
    } else {
        nodesStore.addToSelectedNodes(generateNodeId(node))
    }
};


const handleAddNode = (newNode: ApiNode) => {
    nodesStore.setNode(newNode)
    showAddNodeModal.value = false;
};

const editNode = (node: ApiNode) => {
    newNode.value = node;
    showAddNodeModal.value = true;
};



const currentNode = ref<null | ApiNode>(null)
const connectNode = (node: ApiNode) => {
    nodesStore.setXtermNode(node)
    router.push({ name: 'xterm' })
};

const openFinder = (node: ApiNode) => {
    finderNode.value = node
}

const exploreNodes = () => {
    if (nodesLength.value === 0) {
        return
    }
    shortcutsStore.ensureLoaded();
    // 导出 v3 配置文件：同时包含节点与快捷命令
    const config: AppConfig = {
        version: 3,
        nodes: nodesStore.nodes,
        shortcuts: shortcutsStore.shortcuts,
    };
    const blob = new Blob([JSON.stringify(config)], { type: 'application/json' });
    const url = URL.createObjectURL(blob);
    const a = document.createElement('a');
    a.href = url;
    a.download = `managi-config-${new Date().getTime()}.json`;
    a.click();
    URL.revokeObjectURL(url);
    handleMsg(t("addNode.exportConfigSucess"));
}

const importNodes = () => {
    // 导入 json 配置文件：支持 v3 {nodes, shortcuts} 与旧版纯 nodes 字典
    const input = document.createElement('input');
    input.type = 'file';
    input.accept = 'application/json';
    input.click()
    input.onchange = () => {
        const file = input.files?.[0];
        if (file) {
            const reader = new FileReader();
            reader.onload = () => {
                try {
                    const raw = JSON.parse(reader.result as string);
                    if (typeof raw !== 'object' || raw === null) {
                        handleError(t("addNode.importConfigError"));
                        return;
                    }

                    // 区分 v3 配置文件与旧版纯 nodes 字典
                    const isV3Config = Object.prototype.hasOwnProperty.call(raw, 'nodes');
                    const inputNodes = isV3Config
                        ? (raw.nodes as Record<string, ApiNode | OldApiNode>)
                        : (raw as Record<string, ApiNode | OldApiNode>);
                    const inputShortcuts = isV3Config ? (raw.shortcuts as ShortcutItem[] | undefined) : undefined;

                    // 校验节点必填字段
                    for (const key1 in inputNodes) {
                        const requiredKeys = ['port', 'auth_type', 'auth_value'];
                        for (const key2 of requiredKeys) {
                            if (!Object.prototype.hasOwnProperty.call(inputNodes[key1], key2)) {
                                handleError(`${t("addNode.importConfigError")} -> [${key1}].${key2} `);
                                return;
                            }
                        }
                    }

                    // 校验快捷命令
                    if (inputShortcuts !== undefined) {
                        if (!Array.isArray(inputShortcuts)) {
                            handleError(t("addNode.importConfigError"));
                            return;
                        }
                        for (let i = 0; i < inputShortcuts.length; i++) {
                            const sc = inputShortcuts[i];
                            if (typeof sc?.label !== 'string' || typeof sc?.cmd !== 'string') {
                                handleError(`${t("addNode.importConfigError")} -> shortcuts[${i}]`);
                                return;
                            }
                        }
                    }

                    // 写入节点
                    nodesStore.setAllNodes(Object.values(inputNodes).map(oldApiNodeConvert));
                    setCachedNodes(Object.values(nodesStore.nodes));

                    // 写入快捷命令（如存在）
                    if (inputShortcuts) {
                        shortcutsStore.setAll(inputShortcuts);
                    }

                    handleMsg(t("addNode.importConfigSucess"));
                } catch (error) {
                    handleError(`${t("addNode.importConfigError")} ${error}`);
                }
            };
            reader.readAsText(file);
        }
    };
};

// N2 修正：v3 nodesStore.removeNode 接收 id: string。
const confirmDelete = (node: ApiNode) => {
    if (confirm('确定要删除该节点吗？')) {
        nodesStore.removeNode(generateNodeId(node))
    }
};



watch(nodesStore.nodes, () => {
    // v3 setCachedNodes 接收 ApiNode[]，nodesStore.nodes 是 Record，需 Object.values
    setCachedNodes(Object.values(nodesStore.nodes));
}, { deep: true });


</script>

<style scoped>
.node-list-container {
    position: fixed;
    left: 0;
    top: 0;
    height: 100%;
    width: var(--sidebar-width, 20rem);
    z-index: 2;
    background: var(--color-bg);
    transition: width 0.2s ease-in-out;
}

.sidebar-toggle {
    position: fixed;
    bottom: 0;
    left: 0;
    z-index: 10;
    display: flex;
    align-items: center;
    justify-content: center;
    width: 1.75rem;
    height: 3rem;
    padding: 0;
    color: var(--color-font-1);
    background: transparent;
    /* background: var(--color-bg); */
    border: none;
    /* border-right: 1px solid var(--color-sub);
    border-bottom: 1px solid var(--color-sub); */
    border-radius: 0;
    cursor: pointer;
}

.sidebar-toggle:hover {
    color: var(--color-main);
}

.node-list {
    display: flex;
    flex-direction: column;
    justify-content: space-between;
    border-right: 1px solid var(--color-sub);
    height: 100%;
    width: 20rem;
}



.header-title {
    font-size: 0.9rem;
    flex-shrink: 0;
    color: var(--color-font-1);

}



.nodes {

    list-style: none;
    padding: 0.5rem 0.25rem;
    height: calc(100% - 6rem);
    overflow: auto;
    box-shadow: rgba(0, 0, 0, 0.06) 0px 2px 4px 0px inset;
}

.node {
    position: relative;
    display: flex;
    align-items: center;
    justify-content: space-between;
    margin-bottom: 0.5rem;
    padding: 0.5rem;
    cursor: pointer;
    border-radius: 4px;
    border: 1px solid var(--color-sub);
}

.node:hover {
    background-color: var(--color-sub);
    border: 1px solid var(--color-main);
}

.node-info {
    display: flex;
    align-items: center;
}

.node-info>input {
    min-width: 0;
    width: 1rem;
    flex-shrink: 0;
    cursor: pointer;
    margin-right: 0.15rem;
}

.node-info-name {
    width: 100%;
    white-space: nowrap;
    font-size: clamp(0.6rem, 0.9rem, 1rem);
    overflow: hidden;
    color: var(--color-font-2);

}

.node-actions {
    display: none;
    gap: 0.35rem;
    width: auto;

    position: absolute;
    right: 0.5rem;
    opacity: 1;
    transition: opacity 0.5s ease-in;
    padding-left: 0.75rem;
    background: linear-gradient(to right, transparent, var(--color-sub) 0.5rem);

}


.node:hover .node-actions {
    display: flex;
    opacity: 1;
}

.selected {
    color: var(--color-green);
}


.setting-icon {
    width: 1.5rem;
    height: 1.5rem;
}

.ssh-active {
    background-color: var(--color-sub);
}

.footer-title {
    padding-left: 1.25rem;
    font-weight: lighter;
    color: var(--color-font-3);
}

.language-btn {
    width: 1.5rem;
    height: 1.5rem;
    padding: 0;
    display: flex;
    align-items: center;
    justify-content: center;

}

* {
    overflow: hidden;
}
</style>
