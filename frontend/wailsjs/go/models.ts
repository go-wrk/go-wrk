export namespace main {
	
	export class Config {
	    url: string;
	    method: string;
	    concurrency: number;
	    duration: number;
	    requests: number;
	    timeout: number;
	    headers: Record<string, string>;
	    body: string;
	    host: string;
	    noCompression: boolean;
	    noKeepAlive: boolean;
	    skipVerify: boolean;
	    allowRedirects: boolean;
	    http2: boolean;
	    clientCert: string;
	    clientKey: string;
	    caCert: string;
	    recordDetail: boolean;
	
	    static createFrom(source: any = {}) {
	        return new Config(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.url = source["url"];
	        this.method = source["method"];
	        this.concurrency = source["concurrency"];
	        this.duration = source["duration"];
	        this.requests = source["requests"];
	        this.timeout = source["timeout"];
	        this.headers = source["headers"];
	        this.body = source["body"];
	        this.host = source["host"];
	        this.noCompression = source["noCompression"];
	        this.noKeepAlive = source["noKeepAlive"];
	        this.skipVerify = source["skipVerify"];
	        this.allowRedirects = source["allowRedirects"];
	        this.http2 = source["http2"];
	        this.clientCert = source["clientCert"];
	        this.clientKey = source["clientKey"];
	        this.caCert = source["caCert"];
	        this.recordDetail = source["recordDetail"];
	    }
	}
	export class LatencyBin {
	    lowerMs: number;
	    upperMs: number;
	    count: number;
	
	    static createFrom(source: any = {}) {
	        return new LatencyBin(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.lowerMs = source["lowerMs"];
	        this.upperMs = source["upperMs"];
	        this.count = source["count"];
	    }
	}
	export class NamedConfig {
	    name: string;
	    savedAt: string;
	    config: Config;
	
	    static createFrom(source: any = {}) {
	        return new NamedConfig(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.name = source["name"];
	        this.savedAt = source["savedAt"];
	        this.config = this.convertValues(source["config"], Config);
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class RequestRecord {
	    seq: number;
	    at: number;
	    startMs: number;
	    method: string;
	    url: string;
	    status: number;
	    latencyMs: number;
	    size: number;
	    error?: string;
	    reqHeaders?: Record<string, string>;
	    respHeaders?: Record<string, string>;
	    reqBody?: string;
	    respBody?: string;
	    bodyCut?: boolean;
	
	    static createFrom(source: any = {}) {
	        return new RequestRecord(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.seq = source["seq"];
	        this.at = source["at"];
	        this.startMs = source["startMs"];
	        this.method = source["method"];
	        this.url = source["url"];
	        this.status = source["status"];
	        this.latencyMs = source["latencyMs"];
	        this.size = source["size"];
	        this.error = source["error"];
	        this.reqHeaders = source["reqHeaders"];
	        this.respHeaders = source["respHeaders"];
	        this.reqBody = source["reqBody"];
	        this.respBody = source["respBody"];
	        this.bodyCut = source["bodyCut"];
	    }
	}
	export class Snapshot {
	    running: boolean;
	    elapsed: number;
	    requests: number;
	    errors: number;
	    bytes: number;
	    qps: number;
	    avgQps: number;
	    minMs: number;
	    maxMs: number;
	    avgMs: number;
	    stdDevMs: number;
	    p50Ms: number;
	    p75Ms: number;
	    p90Ms: number;
	    p99Ms: number;
	    statusCodes: Record<string, number>;
	    errorMap: Record<string, number>;
	    latencyBins: LatencyBin[];
	
	    static createFrom(source: any = {}) {
	        return new Snapshot(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.running = source["running"];
	        this.elapsed = source["elapsed"];
	        this.requests = source["requests"];
	        this.errors = source["errors"];
	        this.bytes = source["bytes"];
	        this.qps = source["qps"];
	        this.avgQps = source["avgQps"];
	        this.minMs = source["minMs"];
	        this.maxMs = source["maxMs"];
	        this.avgMs = source["avgMs"];
	        this.stdDevMs = source["stdDevMs"];
	        this.p50Ms = source["p50Ms"];
	        this.p75Ms = source["p75Ms"];
	        this.p90Ms = source["p90Ms"];
	        this.p99Ms = source["p99Ms"];
	        this.statusCodes = source["statusCodes"];
	        this.errorMap = source["errorMap"];
	        this.latencyBins = this.convertValues(source["latencyBins"], LatencyBin);
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}

}

