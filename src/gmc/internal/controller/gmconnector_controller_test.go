/*
* Copyright (C) 2024-2026 Intel Corporation
* SPDX-License-Identifier: Apache-2.0
 */

package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"

	mcv1alpha3 "erag.intel.com/gmc/api/v1alpha3"
)

var _ = Describe("GMConnector Controller", func() {
	Context("When reconciling a resource", func() {
		const resourceName = "test-resource"

		ctx := context.Background()

		typeNamespacedName := types.NamespacedName{
			Name:      resourceName,
			Namespace: "default", // TODO(user):Modify as needed
		}
		gmconnector := &mcv1alpha3.GMConnector{}

		BeforeEach(func() {
			By("creating the custom resource for the Kind GMConnector")
			err := k8sClient.Get(ctx, typeNamespacedName, gmconnector)
			if err != nil && errors.IsNotFound(err) {
				resource := &mcv1alpha3.GMConnector{
					TypeMeta: metav1.TypeMeta{
						APIVersion: "gmc.erag.intel.com/v1alpha3",
						Kind:       "GMConnector",
					},

					ObjectMeta: metav1.ObjectMeta{
						Name:      resourceName,
						Namespace: "default",
						UID:       "1f9a258c-b7d2-4bb3-9fac-ddf1b4369d24",
					},
					Spec: mcv1alpha3.GMConnectorSpec{
						RouterConfig: mcv1alpha3.RouterConfig{
							Name:        "router",
							ServiceName: "router-service",
							Config: map[string]string{
								"endpoint": "/",
							},
						},
						Nodes: map[string]mcv1alpha3.Router{
							"root": {
								RouterType: "Sequence",
								Steps: []mcv1alpha3.Step{
									{
										StepName: Embedding,
										Data:     "$response",
										Executor: mcv1alpha3.Executor{
											InternalService: mcv1alpha3.GMCTarget{
												ServiceName: "embedding-service",
												Config: map[string]string{
													"endpoint":               "/v1/embeddings",
													"TEI_EMBEDDING_ENDPOINT": "tei-embedding-service",
												},
											},
										},
									},
									{
										StepName: Retriever,
										Executor: mcv1alpha3.Executor{
											InternalService: mcv1alpha3.GMCTarget{
												ServiceName: "retriever-service",
												Config: map[string]string{
													"endpoint":               "/v1/retrv",
													"REDIS_URL":              "vector-service",
													"TEI_EMBEDDING_ENDPOINT": "tei-embedding-service",
												},
											},
										},
									},
									{
										StepName: Reranking,
										Executor: mcv1alpha3.Executor{
											InternalService: mcv1alpha3.GMCTarget{
												ServiceName: "rerank-service",
												Config: map[string]string{
													"endpoint":               "/v1/reranking",
													"TEI_RERANKING_ENDPOINT": "tei-reranking-svc",
												},
											},
										},
									},
									{
										StepName: PromptTemplate,
										Executor: mcv1alpha3.Executor{
											InternalService: mcv1alpha3.GMCTarget{
												ServiceName: "prompt-template-svc",
												Config: map[string]string{
													"endpoint": "/v1/prompt_template",
												},
											},
										},
									},
									{
										StepName: Llm,
										Executor: mcv1alpha3.Executor{
											InternalService: mcv1alpha3.GMCTarget{
												ServiceName: "llm-service",
												Config: map[string]string{
													"endpoint":         "/v1/llm",
													"TGI_LLM_ENDPOINT": "tgi-service-name",
												},
											},
										},
									},
								},
							},
						},
					},
				}
				Expect(k8sClient.Create(ctx, resource)).To(Succeed())
			}
		})

		AfterEach(func() {
			// TODO(user): Cleanup logic after each test, like removing the resource instance
			resource := &mcv1alpha3.GMConnector{}
			err := k8sClient.Get(ctx, typeNamespacedName, resource)
			Expect(err).NotTo(HaveOccurred())

			By("Cleanup the specific resource instance GMConnector")
			Expect(k8sClient.Delete(ctx, resource)).To(Succeed())
		})
		It("should successfully reconcile the resource", func() {

			By("Reconciling the created resource")
			controllerReconciler := &GMConnectorReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}
			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: typeNamespacedName,
			})

			Expect(err).NotTo(HaveOccurred())
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      "embedding-service",
				Namespace: "default",
			}, &corev1.Service{})).To(Succeed())

			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      "embedding-service-deployment",
				Namespace: "default",
			}, &appsv1.Deployment{})).To(Succeed())

			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      "embedding-usvc",
				Namespace: "default",
			}, &corev1.ServiceAccount{})).To(Succeed())

			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      "embedding-usvc-config",
				Namespace: "default",
			}, &corev1.ConfigMap{})).To(Succeed())

			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      "tei-embedding-service",
				Namespace: "default",
			}, &corev1.Service{})).To(Succeed())

			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      "tei-embedding-service-deployment",
				Namespace: "default",
			}, &appsv1.Deployment{})).To(Succeed())

			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      "tei-config",
				Namespace: "default",
			}, &corev1.ConfigMap{})).To(Succeed())

			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      "vector-service",
				Namespace: "default",
			}, &corev1.Service{})).To(Succeed())

			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      "vector-service-deployment",
				Namespace: "default",
			}, &appsv1.Deployment{})).To(Succeed())

			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      "retriever-service",
				Namespace: "default",
			}, &corev1.Service{})).To(Succeed())

			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      "retriever-service-deployment",
				Namespace: "default",
			}, &appsv1.Deployment{})).To(Succeed())

			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      "retriever-usvc",
				Namespace: "default",
			}, &corev1.ServiceAccount{})).To(Succeed())

			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      "retriever-usvc-config",
				Namespace: "default",
			}, &corev1.ConfigMap{})).To(Succeed())

			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      "rerank-service",
				Namespace: "default",
			}, &corev1.Service{})).To(Succeed())

			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      "rerank-service-deployment",
				Namespace: "default",
			}, &appsv1.Deployment{})).To(Succeed())

			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      "reranking-usvc",
				Namespace: "default",
			}, &corev1.ServiceAccount{})).To(Succeed())

			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      "reranking-usvc-config",
				Namespace: "default",
			}, &corev1.ConfigMap{})).To(Succeed())

			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      "tei-reranking-svc",
				Namespace: "default",
			}, &corev1.Service{})).To(Succeed())

			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      "tei-reranking-svc-deployment",
				Namespace: "default",
			}, &appsv1.Deployment{})).To(Succeed())

			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      "teirerank-config",
				Namespace: "default",
			}, &corev1.ConfigMap{})).To(Succeed())

			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      "prompttemplate-usvc-config",
				Namespace: "default",
			}, &corev1.ConfigMap{})).To(Succeed())

			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      "tgi-service-name",
				Namespace: "default",
			}, &corev1.Service{})).To(Succeed())

			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      "tgi-service-name-deployment",
				Namespace: "default",
			}, &appsv1.Deployment{})).To(Succeed())

			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      "tgi-config",
				Namespace: "default",
			}, &corev1.ConfigMap{})).To(Succeed())

			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      "llm-service",
				Namespace: "default",
			}, &corev1.Service{})).To(Succeed())

			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      "llm-service-deployment",
				Namespace: "default",
			}, &appsv1.Deployment{})).To(Succeed())

			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      "llm-usvc",
				Namespace: "default",
			}, &corev1.ServiceAccount{})).To(Succeed())

			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      "llm-usvc-config",
				Namespace: "default",
			}, &corev1.ConfigMap{})).To(Succeed())

			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      "router-service",
				Namespace: "default",
			}, &corev1.Service{})).To(Succeed())

			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      "router-service-deployment",
				Namespace: "default",
			}, &appsv1.Deployment{})).To(Succeed())

			pipeline := &mcv1alpha3.GMConnector{}
			Expect(k8sClient.Get(ctx, typeNamespacedName, pipeline)).To(Succeed())
			Expect(pipeline.Status.Status).To(Equal("0/0/10"))
			Expect(len(pipeline.Status.Annotations)).To(Equal(32))

		})

		It("should successfully reconcile the deployment for status update", func() {
			controllerReconciler := &GMConnectorReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}
			By("Reconciling the existed resource")
			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: typeNamespacedName,
			})
			Expect(err).NotTo(HaveOccurred())
			embedDp := &appsv1.Deployment{}
			embedDpMeta := types.NamespacedName{
				Name:      "embedding-service-deployment",
				Namespace: "default",
			}
			Expect(k8sClient.Get(ctx, embedDpMeta, embedDp)).To(Succeed())
			embedDp.Status.AvailableReplicas = int32(1)
			embedDp.Status.Replicas = embedDp.Status.AvailableReplicas
			embedDp.Status.ReadyReplicas = embedDp.Status.AvailableReplicas
			Expect(*embedDp.Spec.Replicas).To(Equal(int32(1)))
			Expect(embedDp.OwnerReferences[0].Name).To(Equal(resourceName))
			Expect(embedDp.OwnerReferences[0].Kind).To(Equal("GMConnector"))
			err = k8sClient.Status().Update(ctx, embedDp)
			Expect(err).NotTo(HaveOccurred())
			embedDp2 := &appsv1.Deployment{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      "embedding-service-deployment",
				Namespace: "default",
			}, embedDp2)).To(Succeed())
			Expect(embedDp2.Status.AvailableReplicas).To(Equal(int32(1)))
			_, err = controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: embedDpMeta,
			})
			Expect(err).NotTo(HaveOccurred())
			pipeline := &mcv1alpha3.GMConnector{}
			Expect(k8sClient.Get(ctx, typeNamespacedName, pipeline)).To(Succeed())
			Expect(pipeline.Status.Status).To(Equal("1/0/10"))
		})

		It("should successfully reconcile the deployment for removing step", func() {
			controllerReconciler := &GMConnectorReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}
			By("Reconciling the existed resource")
			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: typeNamespacedName,
			})
			Expect(err).NotTo(HaveOccurred())

			err = k8sClient.Get(ctx, typeNamespacedName, gmconnector)
			Expect(err).NotTo(HaveOccurred())

			resource := &mcv1alpha3.GMConnector{
				TypeMeta:   gmconnector.TypeMeta,
				ObjectMeta: gmconnector.ObjectMeta,
				Spec: mcv1alpha3.GMConnectorSpec{
					RouterConfig: mcv1alpha3.RouterConfig{
						Name:        "router",
						ServiceName: "router-service",
						Config: map[string]string{
							"endpoint": "/",
						},
					},
					Nodes: map[string]mcv1alpha3.Router{
						"root": {
							RouterType: "Sequence",
							Steps: []mcv1alpha3.Step{
								{
									StepName: PromptTemplate,
									Executor: mcv1alpha3.Executor{
										InternalService: mcv1alpha3.GMCTarget{
											ServiceName: "prompt-template-svc",
											Config: map[string]string{
												"endpoint": "/v1/prompt_template",
											},
										},
									},
								},
							},
						},
					},
				},
			}
			Expect(k8sClient.Update(ctx, resource)).To(Succeed())
			_, err = controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: typeNamespacedName,
			})
			Expect(err).NotTo(HaveOccurred())

			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      "embedding-service",
				Namespace: "default",
			}, &corev1.Service{})).NotTo(Succeed())

			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      "embedding-service-deployment",
				Namespace: "default",
			}, &appsv1.Deployment{})).NotTo(Succeed())

			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      "embedding-usvc-config",
				Namespace: "default",
			}, &corev1.ConfigMap{})).NotTo(Succeed())

			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      "tei-embedding-service",
				Namespace: "default",
			}, &corev1.Service{})).To(Succeed())

			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      "tei-embedding-service-deployment",
				Namespace: "default",
			}, &appsv1.Deployment{})).To(Succeed())

			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      "tei-config",
				Namespace: "default",
			}, &corev1.ConfigMap{})).To(Succeed())

			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      "vector-service",
				Namespace: "default",
			}, &corev1.Service{})).To(Succeed())

			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      "vector-service-deployment",
				Namespace: "default",
			}, &appsv1.Deployment{})).To(Succeed())

			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      "retriever-service",
				Namespace: "default",
			}, &corev1.Service{})).NotTo(Succeed())

			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      "retriever-service-deployment",
				Namespace: "default",
			}, &appsv1.Deployment{})).NotTo(Succeed())

			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      "retriever-usvc-config",
				Namespace: "default",
			}, &corev1.ConfigMap{})).NotTo(Succeed())

			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      "rerank-service",
				Namespace: "default",
			}, &corev1.Service{})).NotTo(Succeed())

			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      "rerank-service-deployment",
				Namespace: "default",
			}, &appsv1.Deployment{})).NotTo(Succeed())

			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      "reranking-usvc-config",
				Namespace: "default",
			}, &corev1.ConfigMap{})).NotTo(Succeed())

			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      "prompttemplate-usvc-config",
				Namespace: "default",
			}, &corev1.ConfigMap{})).To(Succeed())

			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      "tei-reranking-svc",
				Namespace: "default",
			}, &corev1.Service{})).To(Succeed())

			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      "tei-reranking-svc-deployment",
				Namespace: "default",
			}, &appsv1.Deployment{})).To(Succeed())

			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      "teirerank-config",
				Namespace: "default",
			}, &corev1.ConfigMap{})).To(Succeed())

			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      "tgi-service-name",
				Namespace: "default",
			}, &corev1.Service{})).To(Succeed())

			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      "tgi-service-name-deployment",
				Namespace: "default",
			}, &appsv1.Deployment{})).To(Succeed())

			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      "tgi-config",
				Namespace: "default",
			}, &corev1.ConfigMap{})).To(Succeed())

			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      "llm-service",
				Namespace: "default",
			}, &corev1.Service{})).NotTo(Succeed())

			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      "llm-service-deployment",
				Namespace: "default",
			}, &appsv1.Deployment{})).NotTo(Succeed())

			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      "llm-uservice-config",
				Namespace: "default",
			}, &corev1.ConfigMap{})).NotTo(Succeed())

			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      "router-service",
				Namespace: "default",
			}, &corev1.Service{})).To(Succeed())

			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      "router-service-deployment",
				Namespace: "default",
			}, &appsv1.Deployment{})).To(Succeed())

			pipeline := &mcv1alpha3.GMConnector{}
			Expect(k8sClient.Get(ctx, typeNamespacedName, pipeline)).To(Succeed())
			Expect(pipeline.Status.Status).To(Equal("0/0/6"))
			Expect(len(pipeline.Status.Annotations)).To(Equal(16))
		})
	})
})

var _ = Describe("Predicate Functions", func() {
	var (
		oldGMConnector *mcv1alpha3.GMConnector
		newGMConnector *mcv1alpha3.GMConnector
		oldDeployment  *appsv1.Deployment
		newDeployment  *appsv1.Deployment
		updateEvent    event.UpdateEvent
	)

	BeforeEach(func() {

		// mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		// 	Scheme: k8sClient.Scheme(),
		// 	// Client: k8sClient,
		// })
		// Expect(err).NotTo(HaveOccurred())

		// // Create a new GMConnectorReconciler
		// reconciler := &GMConnectorReconciler{
		// 	Client: k8sClient,
		// 	// Log:    ctrl.Log.WithName("controllers").WithName("GMConnector"),
		// 	Scheme: mgr.GetScheme(),
		// }

		// // Call the SetupWithManager function
		// err = reconciler.SetupWithManager(mgr)
		// Expect(err).NotTo(HaveOccurred())

		oldGMConnector = &mcv1alpha3.GMConnector{
			Spec: mcv1alpha3.GMConnectorSpec{
				RouterConfig: mcv1alpha3.RouterConfig{
					Name:        "router",
					ServiceName: "router-service",
					Config: map[string]string{
						"endpoint": "/",
					},
				},
				Nodes: map[string]mcv1alpha3.Router{
					"root": {
						RouterType: "Sequence",
						Steps: []mcv1alpha3.Step{
							{
								StepName: Embedding,
								Data:     "$response",
								Executor: mcv1alpha3.Executor{
									InternalService: mcv1alpha3.GMCTarget{
										ServiceName: "embedding-service",
										Config: map[string]string{
											"endpoint":               "/v1/embeddings",
											"TEI_EMBEDDING_ENDPOINT": "tei-embedding-service",
										},
									},
								},
							},
							{
								StepName: Retriever,
								Executor: mcv1alpha3.Executor{
									InternalService: mcv1alpha3.GMCTarget{
										ServiceName: "retriever-service",
										Config: map[string]string{
											"endpoint":               "/v1/retrv",
											"REDIS_URL":              "vector-service",
											"TEI_EMBEDDING_ENDPOINT": "tei-embedding-service",
										},
									},
								},
							},
							{
								StepName: Reranking,
								Executor: mcv1alpha3.Executor{
									InternalService: mcv1alpha3.GMCTarget{
										ServiceName: "rerank-service",
										Config: map[string]string{
											"endpoint":               "/v1/reranking",
											"TEI_RERANKING_ENDPOINT": "tei-reranking-svc",
										},
									},
								},
							},
							{
								StepName: PromptTemplate,
								Executor: mcv1alpha3.Executor{
									InternalService: mcv1alpha3.GMCTarget{
										ServiceName: "prompt-template-svc",
										Config: map[string]string{
											"endpoint": "/v1/prompt_template",
										},
									},
								},
							},
							{
								StepName: Llm,
								Executor: mcv1alpha3.Executor{
									InternalService: mcv1alpha3.GMCTarget{
										ServiceName: "llm-service",
										Config: map[string]string{
											"endpoint":         "/v1/llm",
											"TGI_LLM_ENDPOINT": "tgi-service-name",
										},
									},
								},
							},
						},
					},
				},
			},
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-resource2",
				Namespace: "default2",
				UID:       "1f9a258c-b7d2-4bb3-9fac-ddf1b4369d25",
			},
		}
		newGMConnector = oldGMConnector.DeepCopy()
		oldDeployment = &appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "embedding-service-deployment",
				Namespace: "default2",
				OwnerReferences: []metav1.OwnerReference{
					{
						APIVersion: "gmc.erag.intel.com/v1alpha3",
						Kind:       "GMConnector",
						Name:       "test-resource2",
						UID:        "1f9a258c-b7d2-4bb3-9fac-ddf1b4369d25",
					},
				},
			},
			Status: appsv1.DeploymentStatus{
				AvailableReplicas: 1,
				Conditions: []appsv1.DeploymentCondition{
					{
						Type:   appsv1.DeploymentAvailable,
						Status: corev1.ConditionTrue,
					},
				},
				ReadyReplicas:   1,
				Replicas:        1,
				UpdatedReplicas: 1,
			},
		}
		newDeployment = oldDeployment.DeepCopy()
	})

	Describe("isGMConnectorSpecOrMetadataChanged", func() {
		It("should return true if the spec has changed", func() {
			newGMConnector.Spec.RouterConfig.Name = "newRouter"
			updateEvent = event.UpdateEvent{
				ObjectOld: oldGMConnector,
				ObjectNew: newGMConnector,
			}
			Expect(isGMCSpecOrMetadataChanged(updateEvent)).To(BeTrue())
		})

		It("should return true if the metadata has changed", func() {
			newGMConnector.ObjectMeta.Name = "new-name"
			updateEvent = event.UpdateEvent{
				ObjectOld: oldGMConnector,
				ObjectNew: newGMConnector,
			}
			Expect(isGMCSpecOrMetadataChanged(updateEvent)).To(BeTrue())
		})
		It("should return false if neither spec nor metadata has changed", func() {
			updateEvent = event.UpdateEvent{
				ObjectOld: oldGMConnector,
				ObjectNew: newGMConnector,
			}
			Expect(isGMCSpecOrMetadataChanged(updateEvent)).To(BeFalse())
		})
	})

	Describe("isDeploymentStatusChanged", func() {
		It("should return true if the status has changed", func() {
			newDeployment.Status.Conditions[0].Status = corev1.ConditionFalse
			updateEvent = event.UpdateEvent{
				ObjectOld: oldDeployment,
				ObjectNew: newDeployment,
			}
			Expect(isDeploymentStatusChanged(updateEvent)).To(BeTrue())
		})

		It("should return false if the status has not changed", func() {
			updateEvent = event.UpdateEvent{
				ObjectOld: oldDeployment,
				ObjectNew: newDeployment,
			}
			Expect(isDeploymentStatusChanged(updateEvent)).To(BeFalse())
		})
	})
})

func TestGetServiceURL(t *testing.T) {
	service := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-service",
			Namespace: "default",
		},
		Spec: corev1.ServiceSpec{
			Type: corev1.ServiceTypeClusterIP,
			Ports: []corev1.ServicePort{
				{
					Port: 8080,
				},
			},
		},
	}

	expectedURL := "http://test-service.default.svc:8080"
	actualURL := getServiceURL(service)

	if actualURL != expectedURL {
		t.Errorf("Expected URL: %s, but got: %s", expectedURL, actualURL)
	}
}
func TestIsMetadataChanged(t *testing.T) {
	oldObject := &metav1.ObjectMeta{
		Name:      "fido",
		Namespace: "sprint",
		Labels: map[string]string{
			"key1": "value1",
			"key2": "value2",
		},
		Generation: 1,
	}

	newObject := &metav1.ObjectMeta{
		Name:      "dido",
		Namespace: "sprint",
		Labels: map[string]string{
			"key1": "value1",
			"key2": "value2",
		},
		Generation: 1,
	}

	changed := isMetadataChanged(nil, newObject)
	if !changed {
		t.Errorf("Expected metadata changes to be detected, but got false")
	}

	changed = isMetadataChanged(oldObject, nil)
	if !changed {
		t.Errorf("Expected metadata changes to be detected, but got false")
	}

	//check name
	changed = isMetadataChanged(oldObject, newObject)
	if !changed {
		t.Errorf("Expected metadata changes to be detected, but got false")
	}

	//check name space
	newObject = &metav1.ObjectMeta{
		Name:      "fido",
		Namespace: "coca-cola",
		Labels: map[string]string{
			"key1": "value1",
			"key2": "value2",
		},
	}
	changed = isMetadataChanged(oldObject, newObject)
	if !changed {
		t.Errorf("Expected metadata changes to not be detected, but got true")
	}

	// check label
	newObject = &metav1.ObjectMeta{
		Name:      "fido",
		Namespace: "sprint",
		Labels: map[string]string{
			"key1": "value1",
			"key2": "value2",
			"key3": "value3",
		},
		DeletionTimestamp: &metav1.Time{
			Time: time.Now(),
		},
	}
	changed = isMetadataChanged(oldObject, newObject)
	if !changed {
		t.Errorf("Expected metadata changes to not be detected, but got true")
	}

	newObject.Labels = map[string]string{
		"key1": "value1",
		"key2": "value4",
	}
	changed = isMetadataChanged(oldObject, newObject)
	if !changed {
		t.Errorf("Expected metadata changes to not be detected, but got true")
	}

	newObject.Labels = map[string]string{
		"key1": "value1",
	}
	changed = isMetadataChanged(oldObject, newObject)
	if !changed {
		t.Errorf("Expected metadata changes to not be detected, but got true")
	}

	// check deletion timestamp
	newObject = &metav1.ObjectMeta{
		Name:      "fido",
		Namespace: "sprint",
		Labels: map[string]string{
			"key1": "value1",
			"key2": "value2",
		},
		DeletionTimestamp: &metav1.Time{
			Time: time.Now(),
		},
	}
	changed = isMetadataChanged(oldObject, newObject)
	if !changed {
		t.Errorf("Expected metadata changes to not be detected, but got true")
	}

	// check annotation
	newObject = &metav1.ObjectMeta{
		Name:      "fido",
		Namespace: "sprint",
		Labels: map[string]string{
			"key1": "value1",
			"key2": "value2",
		},
		Annotations: map[string]string{
			"key1": "value1",
			"key2": "value2",
		},
	}
	changed = isMetadataChanged(oldObject, newObject)
	if changed {
		t.Errorf("Expected metadata changes to not be detected, but got true")
	}

	// check generation
	newObject = &metav1.ObjectMeta{
		Name:      "fido",
		Namespace: "sprint",
		Labels: map[string]string{
			"key1": "value1",
			"key2": "value2",
		},
		Generation: 2,
	}
	changed = isMetadataChanged(oldObject, newObject)
	if changed {
		t.Errorf("Expected metadata changes to not be detected, but got true")
	}
}

// The tests below pin the order in which a step's Config map is rendered into
// container ENVs, for Deployments and StatefulSets alike. Both sites used to walk
// the map directly, and Go randomizes map iteration order, so an *unchanged*
// GMConnector could render the same ENVs in a new order on any reconcile. That
// alone changes the pod-template-hash, so every pod of the pipeline rolls for no
// actual change - churn rather than an outage under RollingUpdate, but the model
// servers here still reload weights and re-warm caches on the way back up.
//
// Plain go tests: unlike the Ginkgo suite in this file they use a fake client and
// need no envtest binaries.
//
//	go test ./internal/controller/ -run 'EnvInStableOrder|Fingerprint' -count=1

// renderRounds is how many times a test reconciles the same, unchanged step. A
// single round proves nothing since order is drawn per reconcile; this many makes
// a broken ordering near-certain to show up and a correct one impossible to fail.
const renderRounds = 20

const retrieverDeploymentTemplate = `
apiVersion: apps/v1
kind: Deployment
metadata:
  name: retriever-usvc
spec:
  replicas: 1
  selector:
    matchLabels:
      app: retriever-usvc
  template:
    metadata:
      labels:
        app: retriever-usvc
    spec:
      containers:
        - name: retriever-usvc
          image: retriever-usvc:latest
          env:
            - name: FROM_THE_CHART
              value: "must-stay-first"
`

const llmStatefulSetTemplate = `
apiVersion: apps/v1
kind: StatefulSet
metadata:
  name: llm-usvc
spec:
  replicas: 1
  serviceName: llm-svc
  selector:
    matchLabels:
      app: llm-usvc
  template:
    metadata:
      labels:
        app: llm-usvc
    spec:
      containers:
        - name: llm-usvc
          image: llm-usvc:latest
          env:
            - name: FROM_THE_CHART
              value: "must-stay-first"
`

// newRenderReconciler returns a reconciler backed by a fake client that serves
// the given manifest out of the gmc-config ConfigMap, plus the owning graph.
func newRenderReconciler(t *testing.T, templateName, templateYaml string) (*GMConnectorReconciler, *mcv1alpha3.GMConnector) {
	t.Helper()

	testScheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(testScheme); err != nil {
		t.Fatalf("failed to register built-in types: %v", err)
	}
	if err := mcv1alpha3.AddToScheme(testScheme); err != nil {
		t.Fatalf("failed to register GMConnector: %v", err)
	}

	// getTemplateBytes looks the manifest up in the gmc-config ConfigMap of the
	// controller's own namespace before falling back to the filesystem.
	gmcConfig := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: GMCConfigMapName, Namespace: gmcNs},
		Data:       map[string]string{templateName: templateYaml},
	}
	graph := &mcv1alpha3.GMConnector{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "render-test",
			Namespace: "default",
			UID:       "1f9a258c-b7d2-4bb3-9fac-ddf1b4369d26",
		},
	}

	client := fake.NewClientBuilder().WithScheme(testScheme).WithObjects(gmcConfig, graph).Build()
	return &GMConnectorReconciler{Client: client, Scheme: testScheme}, graph
}

// containerEnvNames returns the ENV names of the first container of a rendered
// Deployment or StatefulSet, in the order the controller wrote them.
func containerEnvNames(t *testing.T, obj *unstructured.Unstructured) []string {
	t.Helper()

containers, found, err := unstructured.NestedSlice(obj.Object, "spec", "template", "spec", "containers")
if err != nil || !found || len(containers) == 0 {
	t.Fatalf("no containers in rendered %s/%s: found=%v err=%v", obj.GetKind(), obj.GetName(), found, err)
}
container0, ok := containers[0].(map[string]interface{})
if !ok {
	t.Fatalf("unexpected container type %T in rendered %s/%s", containers[0], obj.GetKind(), obj.GetName())
}
env, found, err := unstructured.NestedSlice(container0, "env")
if err != nil || !found {
	t.Fatalf("no env in rendered %s/%s: found=%v err=%v", obj.GetKind(), obj.GetName(), found, err)
}

	names := make([]string, 0, len(env))
	for _, e := range env {
		names = append(names, e.(map[string]interface{})["name"].(string))
	}
	return names
}

func firstObjectOfKind(t *testing.T, objs []*unstructured.Unstructured, kind string) *unstructured.Unstructured {
	t.Helper()
	for _, obj := range objs {
		if obj.GetKind() == kind {
			return obj
		}
	}
	t.Fatalf("no %s among the %d rendered objects", kind, len(objs))
	return nil
}

// assertStableEnvOrder reconciles the same step renderRounds times and reports
// every distinct ENV ordering it produced.
func assertStableEnvOrder(t *testing.T, r *GMConnectorReconciler, graph *mcv1alpha3.GMConnector, step *mcv1alpha3.Step, kind string) {
	t.Helper()

	node := &mcv1alpha3.Router{RouterType: "Sequence", Steps: []mcv1alpha3.Step{*step}}
	orderings := map[string]int{}
	contents := map[string]int{}

	for round := 0; round < renderRounds; round++ {
		objs, err := r.reconcileResource(context.Background(), "default", step, node, graph)
		if err != nil {
			t.Fatalf("round %d: reconcileResource failed: %v", round, err)
		}
		names := containerEnvNames(t, firstObjectOfKind(t, objs, kind))
		orderings[strings.Join(names, ",")]++

		sorted := append([]string(nil), names...)
		sort.Strings(sorted)
		contents[strings.Join(sorted, ",")]++
	}

	// Sanity check: the ENVs themselves must be identical every round, otherwise
	// the renders differ for some reason other than ordering and the assertion
	// below would be measuring the wrong thing.
	if len(contents) != 1 {
		t.Fatalf("%s: reconciling an unchanged step rendered %d different sets of ENVs, want 1:\n%s",
			kind, len(contents), formatObservations(contents))
	}

	if len(orderings) != 1 {
		t.Errorf("%s: reconciling an unchanged step %d times rendered %d different ENV orderings, want 1.\n"+
			"Same ENVs, different order => different pod template => new pod-template-hash => spurious rollout.\n%s",
			kind, renderRounds, len(orderings), formatObservations(orderings))
	}
}

func formatObservations(observations map[string]int) string {
	keys := make([]string, 0, len(observations))
	for key := range observations {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	var b strings.Builder
	for _, key := range keys {
		fmt.Fprintf(&b, "  %2dx [%s]\n", observations[key], key)
	}
	return b.String()
}

// TestReconcileResourceRendersDeploymentEnvInStableOrder pins the Deployment
// branch of reconcileResource. Drop the sortedKeys call there and it fails: the
// ENVs come out in randomized map order.
func TestReconcileResourceRendersDeploymentEnvInStableOrder(t *testing.T) {
	r, graph := newRenderReconciler(t, "retriever-usvc.yaml", retrieverDeploymentTemplate)

	step := &mcv1alpha3.Step{
		StepName: Retriever,
		Executor: mcv1alpha3.Executor{
			InternalService: mcv1alpha3.GMCTarget{
				ServiceName: "retriever-svc",
				// The params* keys are what the fingerprint layer injects in the
				// ChatQnA pipeline; swapping just those two was enough to roll the
				// pods on a live cluster. The rest are entropy, so a broken
				// ordering cannot look stable by luck across renderRounds.
				Config: map[string]string{
					"endpoint":          "/v1/retrieval",
					"paramsKey":         "chatqna",
					"paramsKind":        "gmconnectors",
					"REDIS_URL":         "redis-vector-db",
					"INDEX_NAME":        "rag-redis",
					"LOGFLAG":           "false",
					"OTEL_SERVICE_NAME": "retriever-usvc",
				},
			},
		},
	}

	assertStableEnvOrder(t, r, graph, step, Deployment)
}

// TestReconcileResourceRendersStatefulSetEnvInStableOrder pins the StatefulSet
// branch, which carries its own copy of the same loop - sorting only the
// Deployment one leaves StatefulSet-backed services flapping.
func TestReconcileResourceRendersStatefulSetEnvInStableOrder(t *testing.T) {
	r, graph := newRenderReconciler(t, "llm-usvc.yaml", llmStatefulSetTemplate)

	step := &mcv1alpha3.Step{
		StepName: Llm,
		Executor: mcv1alpha3.Executor{
			InternalService: mcv1alpha3.GMCTarget{
				ServiceName: "llm-svc",
				Config: map[string]string{
					"endpoint":             "/v1/chat/completions",
					"paramsKey":            "chatqna",
					"paramsKind":           "gmconnectors",
					"MAX_MODEL_LEN":        "8192",
					"MODEL_ID":             "llama3-8b-awq",
					"TENSOR_PARALLEL_SIZE": "1",
					"LLM_MODEL_ID":         "llama3-8b-awq",
				},
			},
		},
	}

	assertStableEnvOrder(t, r, graph, step, StatefulSet)
}

// TestEnvOrderChangesThePodTemplateFingerprint shows why a cosmetic reordering is
// not free: the same ENVs in a different order are a different pod template, and
// pod-template-hash derives from that. The fingerprint below is not
// kube-controller-manager's ComputeHash, but shares the property that matters -
// it is order-sensitive over the ENV slice.
func TestEnvOrderChangesThePodTemplateFingerprint(t *testing.T) {
	paramsKey := corev1.EnvVar{Name: "paramsKey", Value: "chatqna"}
	paramsKind := corev1.EnvVar{Name: "paramsKind", Value: "gmconnectors"}
	withEnv := func(env ...corev1.EnvVar) []corev1.Container {
		return []corev1.Container{{Name: "retriever-usvc", Image: "retriever-usvc:latest", Env: env}}
	}

	oneWay := withEnv(paramsKey, paramsKind)
	otherWay := withEnv(paramsKind, paramsKey)

	if reflect.DeepEqual(oneWay, otherWay) {
		t.Fatalf("expected the two orderings to differ, got identical containers")
	}
	if fingerprint(t, oneWay) == fingerprint(t, otherWay) {
		t.Errorf("expected reordered ENVs to change the pod template fingerprint, but it stayed %s", fingerprint(t, oneWay))
	}
}

func fingerprint(t *testing.T, containers []corev1.Container) string {
	t.Helper()

	encoded, err := json.Marshal(corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: containers}})
	if err != nil {
		t.Fatalf("failed to encode pod template: %v", err)
	}
	hasher := fnv.New32a()
	if _, err := hasher.Write(encoded); err != nil {
		t.Fatalf("failed to hash pod template: %v", err)
	}
	return fmt.Sprintf("%x", hasher.Sum32())
}

func TestHandleStatusUpdateReturnsCollectStatusError(t *testing.T) {
	r, graph := newRenderReconciler(t, "unused", "")
	deployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:            "llm-svc-deployment",
			Namespace:       graph.Namespace,
			OwnerReferences: []metav1.OwnerReference{{Kind: "GMConnector", Name: graph.Name}},
		},
	}

	if _, err := r.handleStatusUpdate(context.Background(), deployment); err == nil {
		t.Fatal("expected the status collection error to be returned so the update is retried, got nil")
	}
}
