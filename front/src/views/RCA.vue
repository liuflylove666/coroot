<template>
    <Views :loading="loading" :error="error" :noTitle="noTitle">
        <template #subtitle>{{ $utils.appId(appId).name }}</template>

        <v-alert v-if="rca === 'not implemented'" color="info" outlined text class="mt-5">
            Root Cause Analysis is not available for this project.
        </v-alert>

        <div v-else-if="rca">
            <!-- SLI Charts -->
            <template v-if="rca.latency_chart || rca.errors_chart">
                <div class="text-h6">
                    Service Level Indicators of
                    <router-link :to="{ name: 'overview', params: { view: 'applications', id: appId }, query: $utils.contextQuery() }" class="name">
                        {{ $utils.appId(appId).name }}
                    </router-link>
                </div>
                <div class="grey--text mb-2 mt-1">
                    <v-icon size="18" style="vertical-align: baseline">mdi-lightbulb-on-outline</v-icon>
                    Select a chart area to identify the root cause of an anomaly
                </div>
                <v-row>
                    <v-col cols="12" md="6">
                        <Chart v-if="rca.latency_chart" :chart="rca.latency_chart" class="my-5 chart" :loading="loading" @select="explainAnomaly" :selection="selection" />
                    </v-col>
                    <v-col cols="12" md="6">
                        <Chart v-if="rca.errors_chart" :chart="rca.errors_chart" class="my-5 chart" :loading="loading" @select="explainAnomaly" :selection="selection" />
                    </v-col>
                </v-row>
            </template>

            <!-- ===== Structured RCA Results (builtin analysis) ===== -->
            <div v-if="rca.summary && (hasCausalData || hasDependencyPeers)" class="rca-results mt-5">

                <!-- Summary Banner -->
                <div class="summary-banner" v-if="topFinding">
                    <div class="banner-icon">
                        <v-icon :color="topFinding.severity >= 2 ? 'red' : 'orange'">mdi-alert-circle</v-icon>
                    </div>
                    <div class="banner-body">
                        <div class="banner-title">{{ rca.summary.short_summary }}</div>
                        <div class="banner-meta">
                            <span>{{ rca.summary.causal_findings.length }} issues detected</span>
                            <span v-if="rca.summary.ranked_causes">· {{ rca.summary.ranked_causes.length }} metrics ranked</span>
                        </div>
                    </div>
                    <div class="banner-confidence">
                        <div class="banner-conf-value">{{ topFinding.confidence }}%</div>
                        <div class="banner-conf-label">Top confidence</div>
                    </div>
                </div>

                <!-- Dependency graph context (from service map) -->
                <v-row v-if="hasDependencyPeers" class="mt-3">
                    <v-col cols="12">
                        <div class="panel dep-peers-panel">
                            <div class="panel-hd">
                                <v-icon small class="mr-1" color="teal">mdi-graph-outline</v-icon>
                                <span class="panel-t">依赖图上下文</span>
                                <span class="panel-sub">来自服务地图的上下游</span>
                                <router-link
                                    :to="{ name: 'overview', params: { view: 'map' }, query: $utils.contextQuery() }"
                                    class="dep-peers-map-link ml-2"
                                >
                                    打开服务地图<v-icon x-small class="ml-1">mdi-open-in-new</v-icon>
                                </router-link>
                            </div>
                            <div class="dep-peers-table-wrap">
                                <table class="dep-peers-tbl">
                                    <thead>
                                        <tr>
                                            <th>方向</th>
                                            <th>服务</th>
                                            <th>应用状态</th>
                                            <th>连接</th>
                                            <th>说明</th>
                                        </tr>
                                    </thead>
                                    <tbody>
                                        <tr v-for="(p, i) in rca.summary.dependency_peers" :key="i">
                                            <td><span class="dep-dir">{{ p.direction }}</span></td>
                                            <td class="dep-name">{{ p.name }}</td>
                                            <td><span class="dep-st" :class="'dep-st-' + (p.app_status || 'unknown')">{{ p.app_status || '—' }}</span></td>
                                            <td><span class="dep-st" :class="'dep-st-' + (p.connection_status || 'unknown')">{{ p.connection_status || '—' }}</span></td>
                                            <td class="dep-hint">{{ p.connection_hint || '—' }}</td>
                                        </tr>
                                    </tbody>
                                </table>
                            </div>
                        </div>
                    </v-col>
                </v-row>

                <!-- Two-column analysis -->
                <v-row class="mt-4" v-if="hasCausalData">
                    <!-- Left: Causal Findings -->
                    <v-col cols="12" md="6">
                        <div class="panel">
                            <div class="panel-hd">
                                <v-icon small class="mr-1" color="amber">mdi-chart-timeline-variant-shimmer</v-icon>
                                <span class="panel-t">可观测因果分析</span>
                                <span class="panel-sub">置信度 · 因果链路综合</span>
                            </div>
                            <div class="findings-scroll">
                                <div
                                    v-for="(f, i) in displayedFindings"
                                    :key="i"
                                    class="f-item"
                                    :class="{ 'f-expanded': expandedFinding === i }"
                                    @click="expandedFinding = expandedFinding === i ? -1 : i"
                                >
                                    <div class="f-row">
                                        <span class="f-rank">{{ i + 1 }}</span>
                                        <div class="f-body">
                                            <span class="f-title">{{ f.title }}</span>
                                            <div class="f-chips" v-if="f.services && f.services.length">
                                                <span v-for="s in f.services" :key="s" class="f-svc">{{ s }}</span>
                                            </div>
                                        </div>
                                        <router-link
                                            v-if="findingLink(f)"
                                            :to="findingLink(f)"
                                            class="f-link"
                                            :title="findingLinkLabel(f)"
                                            @click.native.stop
                                        >
                                            <v-icon x-small>{{ findingLinkIcon(f) }}</v-icon>
                                        </router-link>
                                        <span class="f-sev" :class="sevCls(f.severity)">{{ sevTxt(f.severity) }}</span>
                                        <span class="f-conf">{{ f.confidence }}%</span>
                                    </div>
                                    <div class="f-bar"><div class="f-fill" :class="confCls(f.confidence)" :style="{ width: f.confidence + '%' }" /></div>

                                    <v-expand-transition>
                                        <div v-if="expandedFinding === i" class="f-detail">
                                            <div class="fd-section" v-if="f.detail">
                                                <div class="fd-label"><v-icon x-small class="mr-1">mdi-magnify</v-icon>分析详情</div>
                                                <div class="fd-text" v-html="mdRender(f.detail)" />
                                            </div>
                                            <div class="fd-section" v-if="f.fixes && f.fixes.length">
                                                <div class="fd-label"><v-icon x-small class="mr-1" color="green">mdi-wrench</v-icon>修复建议</div>
                                                <ol class="fd-fixes"><li v-for="(fix, fi) in f.fixes" :key="fi">{{ fix }}</li></ol>
                                            </div>
                                            <div class="fd-links" v-if="findingLink(f)">
                                                <router-link :to="findingLink(f)" class="fd-jump" @click.native.stop>
                                                    <v-icon x-small class="mr-1">{{ findingLinkIcon(f) }}</v-icon>{{ findingLinkLabel(f) }}
                                                    <v-icon x-small class="ml-1">mdi-open-in-new</v-icon>
                                                </router-link>
                                            </div>
                                        </div>
                                    </v-expand-transition>
                                </div>

                                <div v-if="rca.summary.causal_findings.length > maxFindings && !showAllFindings" class="f-more" @click.stop="showAllFindings = true">
                                    Show {{ rca.summary.causal_findings.length - maxFindings }} more...
                                </div>
                            </div>
                        </div>
                    </v-col>

                    <!-- Right: Ranked Root Causes -->
                    <v-col cols="12" md="6">
                        <div class="panel">
                            <div class="panel-hd">
                                <v-icon small class="mr-1" color="blue">mdi-test-tube</v-icon>
                                <span class="panel-t">因果关系分析 (BARO/RCA)</span>
                                <span class="stat-badge">HYPOTHESIS TESTING</span>
                            </div>
                            <div class="ranked-scroll" v-if="rca.summary.ranked_causes && rca.summary.ranked_causes.length">
                                <div v-for="(c, i) in rca.summary.ranked_causes" :key="i" class="r-item">
                                    <div class="r-conf" :class="confCls(c.confidence)">{{ c.confidence }}%</div>
                                    <div class="r-body">
                                        <router-link v-if="metricLink(c)" :to="metricLink(c)" class="r-metric r-metric-link" :title="'查看 ' + c.metric + ' 详情'">
                                            <span v-if="c.service" class="r-svc">{{ c.service }}/</span>{{ c.metric }}
                                            <v-icon x-small class="r-ext">mdi-open-in-new</v-icon>
                                        </router-link>
                                        <div v-else class="r-metric"><span v-if="c.service" class="r-svc">{{ c.service }}/</span>{{ c.metric }}</div>
                                        <div class="r-detail">{{ c.detail }}</div>
                                    </div>
                                    <div class="r-bar-wrap"><div class="r-bar" :class="confCls(c.confidence)" :style="{ width: c.confidence + '%' }" /></div>
                                </div>
                            </div>
                            <div v-else class="grey--text text-center pa-4" style="font-size:13px">No ranked causes available</div>

                            <PropagationMap v-if="rca.summary.propagation_map" :applications="rca.summary.propagation_map.applications" class="pa-3" />
                        </div>
                    </v-col>
                </v-row>

                <!-- Bottom: Logs + Traces -->
                <v-row class="mt-4" v-if="hasRelatedLogs || hasRelatedTraces">
                    <v-col cols="12" :md="hasRelatedTraces ? 6 : 12" v-if="hasRelatedLogs">
                        <div class="panel">
                            <div class="panel-hd">
                                <v-icon small class="mr-1" color="orange">mdi-text-box-search-outline</v-icon>
                                <span class="panel-t">关联日志</span>
                            </div>
                            <div class="tbl-wrap">
                                <table class="tbl">
                                    <thead><tr><th>时间</th><th>级别</th><th>服务</th><th>消息</th></tr></thead>
                                    <tbody>
                                        <tr v-for="(l, i) in rca.summary.related_logs" :key="i">
                                            <td class="t-mono t-muted">{{ l.timestamp }}</td>
                                            <td><span class="sev-pill" :class="logCls(l.severity)">{{ l.severity }}</span></td>
                                            <td class="t-svc">
                                                <router-link :to="appLogLink()" class="t-svc-link" title="查看应用日志">{{ l.service }}</router-link>
                                            </td>
                                            <td class="t-msg">{{ l.message }}</td>
                                        </tr>
                                    </tbody>
                                </table>
                            </div>
                        </div>
                    </v-col>
                    <v-col cols="12" :md="hasRelatedLogs ? 6 : 12" v-if="hasRelatedTraces">
                        <div class="panel">
                            <div class="panel-hd">
                                <v-icon small class="mr-1" color="purple">mdi-transit-connection-variant</v-icon>
                                <span class="panel-t">关联链路</span>
                            </div>
                            <div class="tbl-wrap">
                                <table class="tbl">
                                    <thead><tr><th>Trace ID</th><th>服务</th><th>耗时</th><th>时间</th><th>状态</th></tr></thead>
                                    <tbody>
                                        <tr v-for="(t, i) in rca.summary.related_traces" :key="i">
                                            <td class="t-mono">
                                                <router-link v-if="t.trace_id" :to="traceLink(t.trace_id)" class="t-link" title="查看链路详情">{{ t.trace_id }}</router-link>
                                                <span v-else class="t-muted">-</span>
                                            </td>
                                            <td>{{ t.service }}</td>
                                            <td class="t-bold" :class="{ 'sev-err-text': t.status === 'ERROR' }">{{ t.duration }}</td>
                                            <td class="t-mono t-muted">{{ t.time }}</td>
                                            <td><span v-if="t.status === 'ERROR'" class="sev-pill sev-err">{{ t.status }}</span><span v-else>{{ t.status }}</span></td>
                                        </tr>
                                    </tbody>
                                </table>
                            </div>
                        </div>
                    </v-col>
                </v-row>

                <!-- Full text analysis toggle -->
                <div class="mt-4" v-if="rca.summary.root_cause || rca.summary.detailed_root_cause_analysis">
                    <a @click="show_details = !show_details" class="toggle-link">
                        <v-icon small class="mr-1">mdi-text-box-outline</v-icon>
                        {{ show_details ? '收起' : '展开' }}完整文本分析
                        <v-icon small>{{ show_details ? 'mdi-chevron-up' : 'mdi-chevron-down' }}</v-icon>
                    </a>
                    <v-expand-transition>
                        <v-card outlined v-if="show_details" class="pa-5 mt-3">
                            <template v-if="rca.summary.root_cause">
                                <div class="mb-3 text-h6"><v-icon color="red">mdi-fire</v-icon> Root Cause</div>
                                <Markdown :src="rca.summary.root_cause" :widgets="[]" />
                            </template>
                            <template v-if="rca.summary.immediate_fixes">
                                <div class="mt-4 mb-3 text-h6"><v-icon color="red">mdi-fire-extinguisher</v-icon> Immediate Fixes</div>
                                <Markdown :src="rca.summary.immediate_fixes" :widgets="[]" />
                            </template>
                            <template v-if="rca.summary.detailed_root_cause_analysis">
                                <div class="mt-4">
                                    <Markdown :src="rca.summary.detailed_root_cause_analysis" :widgets="rca.summary.widgets || []" />
                                </div>
                            </template>
                        </v-card>
                    </v-expand-transition>
                </div>
            </div>

            <!-- ===== Fallback: Official-style markdown display ===== -->
            <div v-else-if="rca.summary">
                <template v-if="rca.summary.root_cause">
                    <div class="mt-5 mb-3 text-h6"><v-icon color="red">mdi-fire</v-icon> Root Cause</div>
                    <Markdown :src="rca.summary.root_cause" :widgets="[]" />
                    <template v-if="rca.summary.detailed_root_cause_analysis">
                        <div>
                            <a @click="show_details = !show_details">
                                Show {{ show_details ? 'less' : 'more' }} details
                                <v-icon v-if="show_details">mdi-chevron-up</v-icon>
                                <v-icon v-else>mdi-chevron-down</v-icon>
                            </a>
                        </div>
                        <v-card outlined v-if="show_details" class="pa-5 mt-5">
                            <PropagationMap v-if="rca.summary.propagation_map" :applications="rca.summary.propagation_map.applications" class="mb-5" />
                            <Markdown :src="rca.summary.detailed_root_cause_analysis" :widgets="rca.summary.widgets || []" />
                        </v-card>
                    </template>
                </template>
                <template v-if="rca.summary.immediate_fixes">
                    <div class="mt-5 mb-3 text-h6"><v-icon color="red">mdi-fire-extinguisher</v-icon> Immediate Fixes</div>
                    <Markdown :src="rca.summary.immediate_fixes" :widgets="[]" />
                </template>
            </div>

            <!-- ===== No summary yet: Investigate button ===== -->
            <div v-else-if="rca.ai_integration_enabled">
                <v-alert v-if="rca.error" color="red" icon="mdi-alert-circle-outline" outlined text class="mt-4">
                    AI analysis failed: {{ rca.error }}
                </v-alert>
                <div class="pa-5" style="position: relative; border-radius: 4px">
                    <div style="filter: blur(5px)">
                        <v-skeleton-loader boilerplate type="article, text"></v-skeleton-loader>
                    </div>
                    <v-overlay absolute opacity="0.1" z-index="1">
                        <v-btn color="primary" @click="get('true')" class="mx-auto" :loading="loading">
                            <v-icon small left>mdi-magnify</v-icon>
                            {{ rca.error ? 'Retry Analysis' : 'Investigate' }}
                        </v-btn>
                    </v-overlay>
                </div>
            </div>
            <div v-else>
                <div class="pa-5" style="position: relative; border-radius: 4px">
                    <div style="filter: blur(7px)">
                        <v-skeleton-loader boilerplate type="article, text"></v-skeleton-loader>
                    </div>
                    <v-overlay absolute opacity="0.1">
                        <v-btn color="primary" :to="{ name: 'project_settings', params: { tab: 'ai' } }" class="mx-auto">
                            <v-icon small left>mdi-creation</v-icon>
                            Enable an AI integration
                        </v-btn>
                    </v-overlay>
                </div>
            </div>
        </div>
    </Views>
</template>

<script>
import Chart from '@/components/Chart.vue';
import Views from '@/views/Views.vue';
import Markdown from '@/components/Markdown.vue';
import PropagationMap from '@/components/PropagationMap.vue';

export default {
    props: {
        appId: String,
        noTitle: Boolean,
    },
    components: { PropagationMap, Markdown, Views, Chart },

    data() {
        return {
            rca: null,
            loading: false,
            show_details: false,
            error: '',
            expandedFinding: -1,
            showAllFindings: false,
            maxFindings: 5,
            selection: { mode: '', from: this.$route.query.rcaFrom || 0, to: this.$route.query.rcaTo || 0 },
        };
    },

    computed: {
        hasCausalData() {
            return this.rca && this.rca.summary && this.rca.summary.causal_findings && this.rca.summary.causal_findings.length > 0;
        },
        hasDependencyPeers() {
            return this.rca && this.rca.summary && this.rca.summary.dependency_peers && this.rca.summary.dependency_peers.length > 0;
        },
        hasRelatedLogs() {
            return this.rca && this.rca.summary && this.rca.summary.related_logs && this.rca.summary.related_logs.length > 0;
        },
        hasRelatedTraces() {
            return this.rca && this.rca.summary && this.rca.summary.related_traces && this.rca.summary.related_traces.length > 0;
        },
        topFinding() {
            if (!this.hasCausalData) return null;
            return this.rca.summary.causal_findings[0];
        },
        displayedFindings() {
            if (!this.hasCausalData) return [];
            const list = this.rca.summary.causal_findings;
            return this.showAllFindings ? list : list.slice(0, this.maxFindings);
        },
    },

    watch: {
        '$route.query'() {
            this.selection.from = this.$route.query.rcaFrom || 0;
            this.selection.to = this.$route.query.rcaTo || 0;
        },
    },

    mounted() {
        this.get();
        this.$events.watch(this, this.get, 'refresh');
    },

    methods: {
        explainAnomaly(s) {
            this.selection.from = s.selection.from;
            this.selection.to = s.selection.to;
            this.$router.push({ query: { ...this.$route.query, rcaFrom: s.selection.from, rcaTo: s.selection.to, ...s.ctx } });
            this.get();
        },
        get(withSummary) {
            this.loading = true;
            this.showAllFindings = false;
            this.expandedFinding = -1;
            this.$api.getRCA(this.appId, withSummary, (data, error) => {
                this.loading = false;
                if (error) {
                    this.error = error;
                    return;
                }
                this.rca = data;
            });
        },
        sevCls(s) { return s >= 2 ? 'sev-crit' : s >= 1 ? 'sev-warn' : 'sev-info'; },
        sevTxt(s) { return s >= 2 ? '严重' : s >= 1 ? '警告' : '提示'; },
        confCls(c) { return c >= 80 ? 'c-high' : c >= 50 ? 'c-med' : 'c-low'; },
        logCls(sev) {
            if (!sev) return '';
            const s = sev.toLowerCase();
            if (s === 'error' || s === 'critical' || s === 'fatal') return 'sev-err';
            if (s === 'warning' || s === 'warn') return 'sev-wrn';
            return 'sev-inf';
        },
        mdRender(text) {
            if (!text) return '';
            return text
                .replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;')
                .replace(/\*\*(.*?)\*\*/g, '<strong>$1</strong>')
                .replace(/`(.*?)`/g, '<code>$1</code>')
                .replace(/\n/g, '<br>');
        },

        appReport(report, query) {
            return {
                name: 'overview',
                params: { view: 'applications', id: this.appId, report },
                query: { ...this.$utils.contextQuery(), ...query },
            };
        },

        findingLink(f) {
            if (!f || !f.category) return null;
            const cat = f.category;
            if (cat === 'Memory') return this.appReport('Memory');
            if (cat === 'CPU') return this.appReport('CPU');
            if (cat === 'Network') return this.appReport('Net');
            if (cat === 'HTTP' || cat === 'Latency') return this.appReport('SLO');
            if (cat === 'Stability') return this.appReport('Instances');
            if (cat === 'Logs') return this.appReport('Logs', { query: JSON.stringify({ source: 'agent', view: 'patterns' }) });
            if (cat === 'Traces') return this.appReport('Tracing');
            if (cat === 'Deployment') return this.appReport('Deployments');
            if (cat === 'Kubernetes') return this.appReport('Instances');
            if (cat === 'Dependency') {
                return { name: 'overview', params: { view: 'map' }, query: this.$utils.contextQuery() };
            }
            if (cat === 'Anomaly') return this.appReport('SLO');
            return null;
        },
        findingLinkIcon(f) {
            if (!f) return 'mdi-open-in-new';
            const cat = f.category;
            if (cat === 'Memory') return 'mdi-memory';
            if (cat === 'CPU') return 'mdi-chip';
            if (cat === 'Network') return 'mdi-lan';
            if (cat === 'HTTP' || cat === 'Latency') return 'mdi-speedometer';
            if (cat === 'Stability' || cat === 'Kubernetes') return 'mdi-package-variant';
            if (cat === 'Logs') return 'mdi-text-box-search-outline';
            if (cat === 'Traces') return 'mdi-transit-connection-variant';
            if (cat === 'Deployment') return 'mdi-rocket-launch';
            if (cat === 'Dependency') return 'mdi-graph-outline';
            return 'mdi-open-in-new';
        },
        findingLinkLabel(f) {
            if (!f) return '';
            const cat = f.category;
            if (cat === 'Memory') return '查看内存详情';
            if (cat === 'CPU') return '查看CPU详情';
            if (cat === 'Network') return '查看网络详情';
            if (cat === 'HTTP' || cat === 'Latency') return '查看SLO指标';
            if (cat === 'Stability' || cat === 'Kubernetes') return '查看实例状态';
            if (cat === 'Logs') return '查看日志详情';
            if (cat === 'Traces') return '查看链路追踪';
            if (cat === 'Deployment') return '查看部署详情';
            if (cat === 'Dependency') return '查看服务地图';
            if (cat === 'Anomaly') return '查看SLO指标';
            return '查看详情';
        },

        metricLink(cause) {
            if (!cause || !cause.metric) return null;
            const m = cause.metric.toLowerCase();
            if (m.includes('cpu') || m.includes('throttl')) return this.appReport('CPU');
            if (m.includes('memory') || m.includes('oom') || m.includes('rss')) return this.appReport('Memory');
            if (m.includes('http') || m.includes('request') || m.includes('latency') || m.includes('error_rate')) return this.appReport('SLO');
            if (m.includes('net') || m.includes('tcp') || m.includes('retransmit') || m.includes('connect')) return this.appReport('Net');
            if (m.includes('restart') || m.includes('instance')) return this.appReport('Instances');
            if (m.includes('disk') || m.includes('storage') || m.includes('io')) return this.appReport('Storage');
            if (m.includes('dns')) return this.appReport('DNS');
            if (m.includes('log')) return this.appReport('Logs', { query: JSON.stringify({ source: 'agent', view: 'patterns' }) });
            if (m.includes('deploy')) return this.appReport('Deployments');
            if (m.includes('k8s') || m.includes('event')) return this.appReport('Instances');
            return this.appReport('SLO');
        },

        appLogLink() {
            return this.appReport('Logs', { query: JSON.stringify({ source: 'agent', view: 'patterns' }) });
        },

        traceLink(traceId) {
            if (!traceId) return {};
            return {
                name: 'overview',
                params: { view: 'traces' },
                query: {
                    ...this.$utils.contextQuery(),
                    query: JSON.stringify({ view: 'traces', trace_id: traceId }),
                },
            };
        },
    },
};
</script>

<style scoped>
/* ===== Summary Banner ===== */
.summary-banner {
    display: flex;
    align-items: center;
    gap: 12px;
    padding: 14px 18px;
    border: 1px solid rgba(128, 128, 128, 0.2);
    border-radius: 8px;
    background: rgba(128, 128, 128, 0.03);
}
.banner-icon { flex-shrink: 0; }
.banner-body { flex: 1; min-width: 0; }
.banner-title { font-size: 15px; font-weight: 600; line-height: 1.4; }
.banner-meta { font-size: 12px; color: grey; margin-top: 2px; }
.banner-meta span + span { margin-left: 4px; }
.banner-confidence { text-align: center; flex-shrink: 0; }
.banner-conf-value { font-size: 28px; font-weight: 800; line-height: 1; }
.banner-conf-label { font-size: 10px; color: grey; margin-top: 2px; text-transform: uppercase; letter-spacing: 0.5px; }

/* ===== Panels ===== */
.panel {
    border: 1px solid rgba(128, 128, 128, 0.2);
    border-radius: 8px;
    overflow: hidden;
    height: 100%;
    display: flex;
    flex-direction: column;
}
.panel-hd {
    display: flex;
    align-items: center;
    flex-wrap: wrap;
    gap: 6px;
    padding: 10px 14px;
    border-bottom: 1px solid rgba(128, 128, 128, 0.12);
    background: rgba(128, 128, 128, 0.03);
    flex-shrink: 0;
}
.panel-t { font-weight: 600; font-size: 14px; }
.panel-sub { font-size: 11px; color: grey; margin-left: auto; }
.stat-badge {
    margin-left: auto;
    background: rgba(33, 150, 243, 0.12);
    color: #2196f3;
    font-size: 10px;
    font-weight: 700;
    padding: 2px 8px;
    border-radius: 10px;
    letter-spacing: 0.5px;
}

.dep-peers-map-link {
    font-size: 12px;
    text-decoration: none;
    color: #26a69a !important;
    display: inline-flex;
    align-items: center;
    margin-left: 8px;
}
.dep-peers-table-wrap {
    overflow-x: auto;
    padding: 0 12px 12px;
}
.dep-peers-tbl {
    width: 100%;
    border-collapse: collapse;
    font-size: 12px;
}
.dep-peers-tbl th,
.dep-peers-tbl td {
    padding: 8px 10px;
    text-align: left;
    border-bottom: 1px solid rgba(128, 128, 128, 0.1);
}
.dep-peers-tbl th {
    color: grey;
    font-weight: 600;
    font-size: 11px;
}
.dep-dir {
    text-transform: capitalize;
}
.dep-name {
    font-weight: 500;
    font-size: 12px;
}
.dep-hint {
    color: grey;
    font-size: 11px;
    max-width: 280px;
}
.dep-st-critical {
    color: #ef5350;
    font-weight: 600;
}
.dep-st-warning {
    color: #ffa726;
}
.dep-st-ok {
    color: #66bb6a;
}

/* ===== Findings list ===== */
.findings-scroll {
    max-height: 420px;
    overflow-y: auto;
    flex: 1;
}
.f-item {
    padding: 8px 14px 6px;
    border-bottom: 1px solid rgba(128, 128, 128, 0.08);
    cursor: pointer;
    transition: background 0.12s;
}
.f-item:hover { background: rgba(128, 128, 128, 0.05); }
.f-item.f-expanded { background: rgba(128, 128, 128, 0.04); }
.f-row {
    display: flex;
    align-items: flex-start;
    gap: 6px;
}
.f-rank {
    font-weight: 700;
    font-size: 12px;
    min-width: 16px;
    color: grey;
    line-height: 20px;
}
.f-body { flex: 1; min-width: 0; }
.f-title {
    font-size: 12.5px;
    font-weight: 500;
    line-height: 1.35;
    display: -webkit-box;
    -webkit-line-clamp: 2;
    -webkit-box-orient: vertical;
    overflow: hidden;
}
.f-chips { display: flex; flex-wrap: wrap; gap: 3px; margin-top: 3px; }
.f-svc {
    font-size: 10px;
    padding: 0 6px;
    border-radius: 8px;
    border: 1px solid rgba(128, 128, 128, 0.3);
    color: #64b5f6;
    line-height: 18px;
    white-space: nowrap;
}
.f-sev {
    font-size: 10px;
    font-weight: 600;
    padding: 1px 7px;
    border-radius: 8px;
    white-space: nowrap;
    flex-shrink: 0;
    line-height: 18px;
}
.sev-crit { background: rgba(244, 67, 54, 0.15); color: #ef5350; }
.sev-warn { background: rgba(255, 152, 0, 0.15); color: #ffa726; }
.sev-info { background: rgba(33, 150, 243, 0.12); color: #42a5f5; }
.f-conf {
    font-size: 15px;
    font-weight: 700;
    min-width: 40px;
    text-align: right;
    flex-shrink: 0;
    line-height: 20px;
}
.f-bar {
    height: 2px;
    background: rgba(128, 128, 128, 0.08);
    border-radius: 1px;
    margin-top: 5px;
    overflow: hidden;
}
.f-fill { height: 100%; border-radius: 1px; transition: width 0.3s; }
.c-high { background: #ef5350; }
.c-med { background: #ffa726; }
.c-low { background: #42a5f5; }

.f-more {
    padding: 8px 14px;
    font-size: 12px;
    color: #42a5f5;
    cursor: pointer;
    text-align: center;
}
.f-more:hover { background: rgba(66, 165, 245, 0.05); }

/* Finding detail expansion */
.f-detail {
    margin-top: 8px;
    padding: 8px 10px;
    background: rgba(128, 128, 128, 0.04);
    border-radius: 6px;
    font-size: 11.5px;
}
.fd-section { margin-bottom: 8px; }
.fd-section:last-child { margin-bottom: 0; }
.fd-label {
    font-weight: 600;
    font-size: 11px;
    margin-bottom: 3px;
    display: flex;
    align-items: center;
    opacity: 0.8;
}
.fd-text { line-height: 1.55; }
.fd-text:deep(code) {
    background: rgba(128, 128, 128, 0.15);
    padding: 0 3px;
    border-radius: 3px;
    font-size: 10.5px;
}
.fd-fixes {
    padding-left: 18px;
    margin: 0;
    line-height: 1.6;
}

/* ===== Ranked causes ===== */
.ranked-scroll {
    max-height: 380px;
    overflow-y: auto;
    flex: 1;
}
.r-item {
    display: flex;
    align-items: center;
    gap: 10px;
    padding: 7px 14px;
    border-bottom: 1px solid rgba(128, 128, 128, 0.08);
}
.r-conf {
    font-size: 15px;
    font-weight: 700;
    min-width: 44px;
    text-align: right;
    flex-shrink: 0;
}
.r-body { flex: 1; min-width: 0; }
.r-metric {
    font-size: 12px;
    font-weight: 500;
    font-family: 'SFMono-Regular', 'Consolas', monospace;
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
}
.r-svc { color: #42a5f5; }
.r-detail { font-size: 10.5px; color: grey; margin-top: 1px; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
.r-bar-wrap {
    width: 60px;
    height: 4px;
    background: rgba(128, 128, 128, 0.08);
    border-radius: 2px;
    overflow: hidden;
    flex-shrink: 0;
}
.r-bar { height: 100%; border-radius: 2px; transition: width 0.3s; }

/* ===== Tables (logs / traces) ===== */
.tbl-wrap { max-height: 280px; overflow: auto; flex: 1; }
.tbl {
    width: 100%;
    border-collapse: collapse;
    font-size: 11.5px;
}
.tbl thead th {
    position: sticky;
    top: 0;
    padding: 7px 10px;
    text-align: left;
    font-weight: 600;
    font-size: 10px;
    text-transform: uppercase;
    letter-spacing: 0.5px;
    border-bottom: 1px solid rgba(128, 128, 128, 0.15);
    z-index: 1;
}
.theme--dark .tbl thead th { background: rgba(30, 30, 30, 0.97); color: rgba(255,255,255,0.5); }
.theme--light .tbl thead th { background: rgba(250, 250, 250, 0.97); color: rgba(0,0,0,0.5); }
.tbl tbody tr { border-bottom: 1px solid rgba(128, 128, 128, 0.06); }
.tbl tbody tr:hover { background: rgba(128, 128, 128, 0.04); }
.tbl td { padding: 5px 10px; vertical-align: top; }
.t-mono { font-family: 'SFMono-Regular', 'Consolas', monospace; font-size: 10.5px; }
.t-muted { opacity: 0.55; }
.t-svc { white-space: nowrap; color: #42a5f5; font-size: 11px; }
.t-msg { max-width: 400px; word-break: break-word; }
.t-bold { font-weight: 600; white-space: nowrap; }
.t-link { color: #64b5f6; }

.sev-pill {
    font-size: 9.5px;
    font-weight: 700;
    padding: 1px 6px;
    border-radius: 6px;
    white-space: nowrap;
}
.sev-err { background: rgba(244, 67, 54, 0.15); color: #ef5350; }
.sev-wrn { background: rgba(255, 152, 0, 0.15); color: #ffa726; }
.sev-inf { background: rgba(33, 150, 243, 0.1); color: #64b5f6; }

/* ===== Links ===== */
.f-link {
    display: flex;
    align-items: center;
    justify-content: center;
    width: 22px;
    height: 22px;
    border-radius: 4px;
    flex-shrink: 0;
    color: #64b5f6;
    transition: background 0.12s;
    text-decoration: none;
}
.f-link:hover { background: rgba(100, 181, 246, 0.12); }
.fd-links { margin-top: 8px; padding-top: 6px; border-top: 1px solid rgba(128, 128, 128, 0.1); }
.fd-jump {
    display: inline-flex;
    align-items: center;
    font-size: 11px;
    font-weight: 500;
    color: #42a5f5;
    text-decoration: none;
    padding: 3px 8px;
    border-radius: 4px;
    transition: background 0.12s;
}
.fd-jump:hover { background: rgba(66, 165, 245, 0.1); text-decoration: none; }

.r-metric-link {
    color: inherit;
    text-decoration: none;
    transition: color 0.12s;
}
.r-metric-link:hover { color: #42a5f5; text-decoration: none; }
.r-ext {
    opacity: 0;
    margin-left: 3px;
    transition: opacity 0.15s;
    vertical-align: middle;
}
.r-item:hover .r-ext { opacity: 0.6; }

.t-svc-link {
    color: #42a5f5;
    text-decoration: none;
}
.t-svc-link:hover { text-decoration: underline; }
.t-link {
    color: #64b5f6;
    text-decoration: none;
}
.t-link:hover { text-decoration: underline; }

.sev-err-text { color: #ef5350; }

.toggle-link {
    display: inline-flex;
    align-items: center;
    font-size: 13px;
    cursor: pointer;
}
</style>
