import { useQuery } from "@tanstack/react-query";
import { coreApi } from "../shared/api/client";
export function useCore(days: number) { return useQuery({ queryKey: ["core", days], queryFn: () => coreApi.bootstrap(days), refetchInterval: 15_000, retry: 1 }); }
